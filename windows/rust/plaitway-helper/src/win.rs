//! The Windows side: the Service Control Manager, the access lists and signatures that decide whether a
//! program may be run elevated, and the consent prompt.

use std::{
    ffi::c_void,
    io,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

use windows::{
    core::PCWSTR,
    Win32::{
        Foundation::*,
        Security::{
            Authorization::{ConvertStringSidToSidW, GetNamedSecurityInfoW, SE_FILE_OBJECT},
            Cryptography::{CertGetNameStringW, CERT_NAME_SIMPLE_DISPLAY_TYPE},
            EqualSid, GetAce, IsWellKnownSid, WinBuiltinAdministratorsSid, WinLocalSystemSid, ACL, DACL_SECURITY_INFORMATION, OWNER_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID,
            WinTrust::{WTHelperGetProvSignerFromChain, WTHelperProvDataFromStateData, WinVerifyTrust, WINTRUST_DATA, WINTRUST_DATA_0, WINTRUST_FILE_INFO, WTD_CHOICE_FILE, WTD_REVOKE_NONE, WTD_STATEACTION_CLOSE, WTD_STATEACTION_VERIFY, WTD_UI_NONE},
        },
        Storage::FileSystem::{GetDriveTypeW, GetFileAttributesW, FILE_ATTRIBUTE_REPARSE_POINT, INVALID_FILE_ATTRIBUTES},
        System::{
            Services::{CloseServiceHandle, OpenSCManagerW, OpenServiceW, QueryServiceStatus, SC_MANAGER_CONNECT, SERVICE_CONTINUE_PENDING, SERVICE_PAUSED, SERVICE_PAUSE_PENDING, SERVICE_QUERY_STATUS, SERVICE_RUNNING, SERVICE_START_PENDING, SERVICE_STATUS, SERVICE_STOPPED, SERVICE_STOP_PENDING},
            Threading::{GetExitCodeProcess, WaitForSingleObject},
        },
        UI::{
            Shell::{ShellExecuteExW, SEE_MASK_NOCLOSEPROCESS, SHELLEXECUTEINFOW},
            WindowsAndMessaging::SW_HIDE,
        },
    },
};

use crate::{ElevatedLauncher, ElevatedOutcome, ElevatedResult, FileSignature, FolderProtection, HelperService, HelperState, HelperTrust};

fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(Some(0)).collect()
}

fn wide_path(p: &Path) -> Vec<u16> {
    wide(&p.to_string_lossy())
}

/// The Service Control Manager, asked with the two rights an ordinary user has: to connect, and to read
/// the status of the service.
pub struct WindowsService {
    pub name: String,
}

impl WindowsService {
    pub fn new(name: &str) -> Self {
        WindowsService { name: name.to_string() }
    }
}

impl HelperService for WindowsService {
    fn query(&self) -> HelperState {
        const SERVICE_DOES_NOT_EXIST: i32 = 1060;
        unsafe {
            let Ok(manager) = OpenSCManagerW(PCWSTR::null(), PCWSTR::null(), SC_MANAGER_CONNECT) else { return HelperState::Unknown };
            let name = wide(&self.name);
            let state = match OpenServiceW(manager, PCWSTR(name.as_ptr()), SERVICE_QUERY_STATUS) {
                Ok(service) => {
                    let mut status = SERVICE_STATUS::default();
                    let state = if QueryServiceStatus(service, &mut status).is_ok() {
                        match status.dwCurrentState {
                            SERVICE_STOPPED => HelperState::Stopped,
                            SERVICE_START_PENDING | SERVICE_CONTINUE_PENDING => HelperState::Starting,
                            SERVICE_RUNNING => HelperState::Running,
                            SERVICE_STOP_PENDING | SERVICE_PAUSE_PENDING | SERVICE_PAUSED => HelperState::Stopping,
                            _ => HelperState::Unknown,
                        }
                    } else {
                        HelperState::Unknown
                    };
                    let _ = CloseServiceHandle(service);
                    state
                }
                Err(e) if e.code().0 & 0xffff == SERVICE_DOES_NOT_EXIST => HelperState::NotInstalled,
                Err(_) => HelperState::Unknown,
            };
            let _ = CloseServiceHandle(manager);
            state
        }
    }
}

// ---- who may change a folder

const DELETE: u32 = 0x0001_0000;
const FILE_DELETE_CHILD: u32 = 0x40;
const WRITE_DAC: u32 = 0x0004_0000;
const WRITE_OWNER: u32 = 0x0008_0000;
const FILE_WRITE_DATA: u32 = 0x2;
const FILE_APPEND_DATA: u32 = 0x4;
const FILE_WRITE_EA: u32 = 0x10;
const GENERIC_WRITE: u32 = 0x4000_0000;
const GENERIC_ALL: u32 = 0x1000_0000;
const RIGHTS_TO_REPLACE: u32 = DELETE | FILE_DELETE_CHILD | WRITE_DAC | WRITE_OWNER;
const RIGHTS_TO_CHANGE_CONTENTS: u32 = RIGHTS_TO_REPLACE | FILE_WRITE_DATA | FILE_APPEND_DATA | FILE_WRITE_EA;
/// The executable and the folder it is in are checked for everything that changes contents (a library
/// next to a program is loaded by it); the folders above only for what replaces them.
const OBJECTS_CHECKED_FOR_CONTENTS: usize = 2;
const TRUSTED_INSTALLER: &str = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464";
const CREATOR_OWNER: &str = "S-1-3-0";
const ACCESS_ALLOWED_ACE_TYPE: u8 = 0;
const INHERIT_ONLY_ACE: u8 = 0x08;

/// A SID from its text form; freed with the process, since there are only two.
fn sid_from(text: &str) -> PSID {
    let w = wide(text);
    let mut sid = PSID::default();
    unsafe {
        let _ = ConvertStringSidToSidW(PCWSTR(w.as_ptr()), &mut sid);
    }
    sid
}

fn is_trusted_account(sid: PSID) -> bool {
    unsafe { IsWellKnownSid(sid, WinLocalSystemSid).as_bool() || IsWellKnownSid(sid, WinBuiltinAdministratorsSid).as_bool() || EqualSid(sid, sid_from(TRUSTED_INSTALLER)).is_ok() }
}

/// Whether an entry that allows `rights` and applies to this object itself names an account outside the
/// trusted ones. Entries that deny are not counted: whoever may change the list can remove them.
fn has_untrusted_writer(dacl: *const ACL, rights: u32) -> bool {
    if dacl.is_null() {
        // No list at all lets everybody do everything.
        return true;
    }
    unsafe {
        let creator_owner = sid_from(CREATOR_OWNER);
        for i in 0..(*dacl).AceCount as u32 {
            let mut ace: *mut c_void = std::ptr::null_mut();
            if GetAce(dacl, i, &mut ace).is_err() || ace.is_null() {
                return true; // an entry that cannot be read is not known to be harmless
            }
            // ACE_HEADER { type: u8, flags: u8, size: u16 }, then mask: u32, then the SID.
            let bytes = ace as *const u8;
            let (kind, flags, mask) = (*bytes, *bytes.add(1), *(bytes.add(4) as *const u32));
            if kind != ACCESS_ALLOWED_ACE_TYPE || flags & INHERIT_ONLY_ACE != 0 {
                continue;
            }
            let sid = PSID(bytes.add(8) as *mut c_void);
            if !is_trusted_account(sid) && EqualSid(sid, creator_owner).is_err() && (mask & rights != 0 || mask & (GENERIC_WRITE | GENERIC_ALL) != 0) {
                return true;
            }
        }
    }
    false
}

fn inspect_object(path: &Path, rights: u32) -> FolderProtection {
    let w = wide_path(path);
    unsafe {
        let attrs = GetFileAttributesW(PCWSTR(w.as_ptr()));
        if attrs == INVALID_FILE_ATTRIBUTES {
            return FolderProtection::Unknown;
        }
        if attrs & FILE_ATTRIBUTE_REPARSE_POINT.0 != 0 {
            return FolderProtection::Unknown; // a link: where it leads is not what was checked
        }
        let (mut owner, mut dacl, mut sd) = (PSID::default(), std::ptr::null_mut::<ACL>(), PSECURITY_DESCRIPTOR::default());
        let status = GetNamedSecurityInfoW(PCWSTR(w.as_ptr()), SE_FILE_OBJECT, OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION, Some(&mut owner), None, Some(&mut dacl), None, &mut sd);
        if status.0 != 0 || owner.is_invalid() {
            return FolderProtection::Unknown;
        }
        let verdict = if !is_trusted_account(owner) || has_untrusted_writer(dacl, rights) { FolderProtection::WritableByUsers } else { FolderProtection::AdminOnly };
        let _ = LocalFree(Some(HLOCAL(sd.0)));
        verdict
    }
}

/// Every object from the volume root to the file: owned by SYSTEM, Administrators or TrustedInstaller, no
/// link, and no entry that lets another account replace it.
fn protection_of_chain(chain: &[PathBuf]) -> FolderProtection {
    for (position, object) in chain.iter().enumerate() {
        let remaining = chain.len() - position;
        let rights = if remaining <= OBJECTS_CHECKED_FOR_CONTENTS {
            RIGHTS_TO_CHANGE_CONTENTS
        } else if position == 0 {
            // Nobody can delete or rename a volume root, and a data volume lets Authenticated Users modify it.
            RIGHTS_TO_REPLACE & !DELETE
        } else {
            RIGHTS_TO_REPLACE
        };
        let verdict = inspect_object(object, rights);
        if verdict != FolderProtection::AdminOnly {
            return verdict;
        }
    }
    FolderProtection::AdminOnly
}

pub struct WindowsHelperTrust;

impl HelperTrust for WindowsHelperTrust {
    fn inspect_folder(&self, executable: &Path) -> FolderProtection {
        let Ok(absolute) = std::path::absolute(executable) else { return FolderProtection::Unknown };
        let text = absolute.to_string_lossy().to_string();
        // A share, or a path in the \\?\ and \\.\ namespaces, says nothing about who may change it.
        if text.starts_with(r"\\") {
            return FolderProtection::NotLocalFixedDisk;
        }
        let Some(root) = absolute.ancestors().last() else { return FolderProtection::Unknown };
        const DRIVE_FIXED: u32 = 3;
        let root_w = wide_path(root);
        if unsafe { GetDriveTypeW(PCWSTR(root_w.as_ptr())) } != DRIVE_FIXED {
            return FolderProtection::NotLocalFixedDisk;
        }
        let mut chain: Vec<PathBuf> = absolute.ancestors().map(Path::to_path_buf).collect();
        chain.reverse();
        protection_of_chain(&chain)
    }

    fn inspect_signature(&self, executable: &Path, expected_publisher: &str) -> FileSignature {
        const TRUST_E_NOSIGNATURE: i32 = 0x800B0100u32 as i32;
        const TRUST_E_SUBJECT_FORM_UNKNOWN: i32 = 0x800B0003u32 as i32;
        const TRUST_E_PROVIDER_UNKNOWN: i32 = 0x800B0001u32 as i32;
        let path = wide_path(executable);
        let mut file = WINTRUST_FILE_INFO { cbStruct: std::mem::size_of::<WINTRUST_FILE_INFO>() as u32, pcwszFilePath: PCWSTR(path.as_ptr()), ..Default::default() };
        let mut data = WINTRUST_DATA {
            cbStruct: std::mem::size_of::<WINTRUST_DATA>() as u32,
            dwUIChoice: WTD_UI_NONE,
            fdwRevocationChecks: WTD_REVOKE_NONE,
            dwUnionChoice: WTD_CHOICE_FILE,
            Anonymous: WINTRUST_DATA_0 { pFile: &mut file },
            dwStateAction: WTD_STATEACTION_VERIFY,
            // Revocation lookups over the network are off.
            dwProvFlags: windows::Win32::Security::WinTrust::WINTRUST_DATA_PROVIDER_FLAGS(0x1000),
            ..Default::default()
        };
        let mut action = windows::core::GUID::from_u128(0x00AAC56B_CD44_11D0_8CC2_00C04FC295EE);
        unsafe {
            let status = WinVerifyTrust(HWND(-1isize as *mut c_void), &mut action, &mut data as *mut WINTRUST_DATA as *mut c_void);
            let result = match status {
                0 => match publisher_of(&data) {
                    Some(p) if p == expected_publisher => FileSignature::Valid,
                    _ => FileSignature::OtherPublisher,
                },
                TRUST_E_NOSIGNATURE | TRUST_E_SUBJECT_FORM_UNKNOWN | TRUST_E_PROVIDER_UNKNOWN => FileSignature::NotSigned,
                _ => FileSignature::Invalid,
            };
            data.dwStateAction = WTD_STATEACTION_CLOSE;
            let _ = WinVerifyTrust(HWND(-1isize as *mut c_void), &mut action, &mut data as *mut WINTRUST_DATA as *mut c_void);
            result
        }
    }
}

/// The subject of the signing certificate, from the state WinVerifyTrust keeps open.
unsafe fn publisher_of(data: &WINTRUST_DATA) -> Option<String> {
    let provider = WTHelperProvDataFromStateData(data.hWVTStateData);
    if provider.is_null() {
        return None;
    }
    let signer = WTHelperGetProvSignerFromChain(provider, 0, false, 0);
    if signer.is_null() || (*signer).csCertChain == 0 || (*signer).pasCertChain.is_null() {
        return None;
    }
    let cert = (*(*signer).pasCertChain).pCert;
    if cert.is_null() {
        return None;
    }
    let mut buf = [0u16; 256];
    let n = CertGetNameStringW(cert, CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, None, Some(&mut buf)) as usize;
    (n > 1).then(|| String::from_utf16_lossy(&buf[..n - 1]))
}

/// The consent prompt of Windows, through `ShellExecuteEx` with the `runas` verb. The window of the program
/// is hidden; its exit code is what comes back.
pub struct ShellElevatedLauncher {
    /// A program that has not ended by then is given up on; it keeps running, as it would after the app closes.
    pub timeout: Duration,
}

impl Default for ShellElevatedLauncher {
    fn default() -> Self {
        ShellElevatedLauncher { timeout: Duration::from_secs(120) }
    }
}

impl ElevatedLauncher for ShellElevatedLauncher {
    fn run(&self, executable: &Path, arguments: &str) -> io::Result<ElevatedResult> {
        const ERROR_CANCELLED: i32 = 1223;
        // A program that is not there must not look like a declined prompt, which is what the shell answers.
        if !executable.exists() {
            return Err(io::Error::new(io::ErrorKind::NotFound, format!("{} does not exist", executable.display())));
        }
        let (file, args) = (wide_path(executable), wide(arguments));
        let mut info = SHELLEXECUTEINFOW {
            cbSize: std::mem::size_of::<SHELLEXECUTEINFOW>() as u32,
            fMask: SEE_MASK_NOCLOSEPROCESS,
            lpVerb: windows::core::w!("runas"),
            lpFile: PCWSTR(file.as_ptr()),
            lpParameters: PCWSTR(args.as_ptr()),
            nShow: SW_HIDE.0,
            ..Default::default()
        };
        unsafe {
            if let Err(e) = ShellExecuteExW(&mut info) {
                let code = e.code().0 & 0xffff;
                // The user said no: nothing ran.
                return if code == ERROR_CANCELLED { Ok(ElevatedResult { outcome: ElevatedOutcome::Declined, exit_code: 0 }) } else { Err(io::Error::from_raw_os_error(code)) };
            }
            let process = info.hProcess;
            let started = Instant::now();
            let result = loop {
                if WaitForSingleObject(process, 200) == WAIT_OBJECT_0 {
                    let mut code = 0u32;
                    break GetExitCodeProcess(process, &mut code).map(|_| ElevatedResult { outcome: ElevatedOutcome::Completed, exit_code: code as i32 }).map_err(|e| io::Error::from_raw_os_error(e.code().0 & 0xffff));
                }
                if started.elapsed() > self.timeout {
                    break Err(io::Error::new(io::ErrorKind::TimedOut, "the elevated program did not end in time"));
                }
            };
            let _ = CloseHandle(process);
            result
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_exe(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("plaitway-helper-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join(name);
        std::fs::write(&p, b"MZ not a program").unwrap();
        p
    }

    #[test]
    fn a_service_that_does_not_exist_is_not_installed() {
        assert_eq!(WindowsService::new("PlaitwayNoSuchService").query(), HelperState::NotInstalled);
    }

    #[test]
    fn a_service_that_always_exists_has_a_state() {
        // The Windows Event Log service runs on every machine.
        assert_eq!(WindowsService::new("EventLog").query(), HelperState::Running);
    }

    #[test]
    fn program_files_is_admin_only_and_a_temporary_folder_is_not() {
        let t = WindowsHelperTrust;
        let notepad = Path::new(r"C:\Windows\System32\notepad.exe");
        if notepad.exists() {
            assert_eq!(t.inspect_folder(notepad), FolderProtection::AdminOnly);
        }
        assert_eq!(t.inspect_folder(&temp_exe("x.exe")), FolderProtection::WritableByUsers);
    }

    #[test]
    fn the_installed_helper_is_trusted_where_it_is() {
        // Program Files is writable only by TrustedInstaller and administrators: the rule must agree.
        let installed = Path::new(r"C:\Program Files\Plaitway\plaitwayd.exe");
        if installed.exists() {
            assert_eq!(WindowsHelperTrust.inspect_folder(installed), FolderProtection::AdminOnly);
        }
    }

    #[test]
    fn a_share_or_a_missing_file_is_refused_for_what_it_is() {
        let t = WindowsHelperTrust;
        assert_eq!(t.inspect_folder(Path::new(r"\\server\share\plaitwayd.exe")), FolderProtection::NotLocalFixedDisk);
        assert_eq!(t.inspect_folder(Path::new(r"C:\Windows\System32\no-such-file-plaitway.exe")), FolderProtection::Unknown);
    }

    #[test]
    fn a_link_in_the_path_is_not_followed() {
        // A junction from a temporary folder to System32: where it leads is not what was checked.
        let base = std::env::temp_dir().join(format!("plaitway-helper-link-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&base);
        std::fs::create_dir_all(&base).unwrap();
        let link = base.join("sys");
        let made = std::process::Command::new("cmd").args(["/c", "mklink", "/J", &link.to_string_lossy(), r"C:\Windows\System32"]).output().map(|o| o.status.success()).unwrap_or(false);
        if made {
            assert_ne!(WindowsHelperTrust.inspect_folder(&link.join("notepad.exe")), FolderProtection::AdminOnly);
        }
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn an_unsigned_file_has_no_signature() {
        assert_eq!(WindowsHelperTrust.inspect_signature(&temp_exe("unsigned.exe"), EXPECTED), FileSignature::NotSigned);
    }

    const EXPECTED: &str = crate::EXPECTED_PUBLISHER;

    #[test]
    fn a_program_signed_by_somebody_else_is_another_publisher() {
        // Any program of this machine with an embedded signature will do.
        let candidates = [r"C:\Program Files\Google\Chrome\Application\chrome.exe", r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe", r"C:\Program Files\Mozilla Firefox\firefox.exe"];
        let Some(signed) = candidates.iter().map(Path::new).find(|p| p.exists()) else {
            eprintln!("skipped: no signed program found");
            return;
        };
        assert_eq!(WindowsHelperTrust.inspect_signature(signed, EXPECTED), FileSignature::OtherPublisher);
    }

    #[test]
    fn a_changed_signed_file_has_an_invalid_signature() {
        let candidates = [r"C:\Program Files\Google\Chrome\Application\chrome.exe", r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe", r"C:\Program Files\Mozilla Firefox\firefox.exe"];
        let Some(signed) = candidates.iter().map(Path::new).find(|p| p.exists()) else {
            return;
        };
        let copy = temp_exe("changed.exe");
        let mut bytes = std::fs::read(signed).unwrap();
        // Change a byte in the middle of the code; the signature covers it.
        let at = bytes.len() / 2;
        bytes[at] ^= 0xFF;
        std::fs::write(&copy, bytes).unwrap();
        assert_eq!(WindowsHelperTrust.inspect_signature(&copy, EXPECTED), FileSignature::Invalid);
    }

    /// Runs a real program elevated; it only asks the user when Windows is set to, so it runs on request.
    #[test]
    fn the_exit_code_of_an_elevated_program_comes_back() {
        if std::env::var_os("PLAITWAY_TEST_ELEVATE").is_none() {
            eprintln!("skipped: PLAITWAY_TEST_ELEVATE is not set");
            return;
        }
        let cmd = Path::new(r"C:\Windows\System32\cmd.exe");
        let r = ShellElevatedLauncher::default().run(cmd, "/c exit 7").unwrap();
        assert_eq!(r, ElevatedResult { outcome: ElevatedOutcome::Completed, exit_code: 7 });
        let r = ShellElevatedLauncher::default().run(cmd, "/c exit 0").unwrap();
        assert_eq!(r.exit_code, 0);
    }

    #[test]
    fn a_program_that_does_not_exist_is_a_launch_error_not_a_decline() {
        if std::env::var_os("PLAITWAY_TEST_ELEVATE").is_none() {
            return;
        }
        let e = ShellElevatedLauncher::default().run(Path::new(r"C:\no\such\program.exe"), "").unwrap_err();
        assert_ne!(e.raw_os_error(), Some(1223));
    }
}



