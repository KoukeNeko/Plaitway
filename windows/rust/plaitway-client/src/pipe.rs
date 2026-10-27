//! Opening the daemon's pipe the way its access list expects, and checking who made it first.
//!
//! The daemon grants interactive users read, write, read-attributes and read-control, nothing more, so
//! `GENERIC_READ | GENERIC_WRITE` (which tokio asks for) is refused. And any local user can create
//! `\\.\pipe\plaitway` while the service is not running, so the pipe's owner is read before anything
//! is sent: SYSTEM, Administrators and the calling user are trusted. The rule is the one of
//! `internal/transport/serverowner_windows.go` and the C# `PipeServerVerifier`.

use std::{ffi::c_void, io};

use tokio::net::windows::named_pipe::NamedPipeClient;
use windows::{
    core::{PCWSTR, PWSTR},
    Win32::{
        Foundation::{CloseHandle, LocalFree, HANDLE, HLOCAL},
        Security::{
            Authorization::{ConvertSidToStringSidW, GetSecurityInfo, SE_KERNEL_OBJECT},
            EqualSid, GetTokenInformation, IsWellKnownSid, TokenUser, WinBuiltinAdministratorsSid, WinLocalSystemSid, OWNER_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, PSID, TOKEN_QUERY, TOKEN_USER,
        },
        Storage::FileSystem::{CreateFileW, FILE_FLAG_OVERLAPPED, FILE_READ_ATTRIBUTES, FILE_READ_DATA, FILE_SHARE_READ, FILE_SHARE_WRITE, FILE_WRITE_DATA, OPEN_EXISTING, READ_CONTROL, SECURITY_IDENTIFICATION, SECURITY_SQOS_PRESENT, SYNCHRONIZE},
        System::Threading::{GetCurrentProcess, OpenProcessToken},
    },
};

/// What the daemon lets an interactive user do with the pipe.
const ACCESS: u32 = FILE_READ_DATA.0 | FILE_WRITE_DATA.0 | FILE_READ_ATTRIBUTES.0 | READ_CONTROL.0 | SYNCHRONIZE.0;

fn refused(why: String) -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, format!("pipe server refused: {why}"))
}

fn win(e: windows::core::Error) -> io::Error {
    io::Error::from_raw_os_error(e.code().0 & 0xffff)
}

/// Open `path` with the daemon's access and the identification impersonation level, and hand it back
/// only if its owner is trusted.
pub fn open(path: &str) -> io::Result<NamedPipeClient> {
    let wide: Vec<u16> = path.encode_utf16().chain(Some(0)).collect();
    let handle = unsafe {
        CreateFileW(PCWSTR(wide.as_ptr()), ACCESS, FILE_SHARE_READ | FILE_SHARE_WRITE, None, OPEN_EXISTING, FILE_FLAG_OVERLAPPED | SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION, None)
    }
    .map_err(win)?;
    if let Err(e) = verify(handle) {
        unsafe {
            let _ = CloseHandle(handle);
        }
        return Err(e);
    }
    // The handle is overlapped, which is what tokio needs; it owns it from here on.
    unsafe { NamedPipeClient::from_raw_handle(handle.0 as *mut c_void) }
}

fn verify(pipe: HANDLE) -> io::Result<()> {
    let (mut owner, mut descriptor) = (PSID::default(), PSECURITY_DESCRIPTOR::default());
    let status = unsafe { GetSecurityInfo(pipe, SE_KERNEL_OBJECT, OWNER_SECURITY_INFORMATION, Some(&mut owner), None, None, None, Some(&mut descriptor)) };
    if status.0 != 0 {
        return Err(refused(format!("cannot read its owner: os error {}", status.0)));
    }
    let result = (|| {
        if owner.is_invalid() {
            return Err(refused("it has no valid owner".into()));
        }
        let caller = caller_sid()?;
        let trusted = owner_is_trusted(owner, PSID(caller.as_ptr() as *mut c_void));
        trusted.then_some(()).ok_or_else(|| refused(format!("it is owned by {}, not by SYSTEM, Administrators or you", sid_text(owner))))
    })();
    unsafe {
        let _ = LocalFree(Some(HLOCAL(descriptor.0)));
    }
    result
}

/// The rule: SYSTEM, Administrators, or the caller's own user.
fn owner_is_trusted(owner: PSID, caller: PSID) -> bool {
    unsafe { IsWellKnownSid(owner, WinLocalSystemSid).as_bool() || IsWellKnownSid(owner, WinBuiltinAdministratorsSid).as_bool() || EqualSid(owner, caller).is_ok() }
}

/// The calling user's SID, as the bytes of a TOKEN_USER (the SID points into them).
fn caller_sid() -> io::Result<Vec<u8>> {
    unsafe {
        let mut token = HANDLE::default();
        OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token).map_err(win)?;
        let mut len = 0u32;
        let _ = GetTokenInformation(token, TokenUser, None, 0, &mut len);
        let mut buf = vec![0u8; len as usize];
        let r = GetTokenInformation(token, TokenUser, Some(buf.as_mut_ptr() as *mut c_void), len, &mut len);
        let _ = CloseHandle(token);
        r.map_err(win)?;
        let sid = (*(buf.as_ptr() as *const TOKEN_USER)).User.Sid;
        // Copy the SID itself out, so the bytes do not depend on the TOKEN_USER around them.
        Ok(sid_bytes(sid))
    }
}

fn sid_bytes(sid: PSID) -> Vec<u8> {
    use windows::Win32::Security::GetLengthSid;
    unsafe { std::slice::from_raw_parts(sid.0 as *const u8, GetLengthSid(sid) as usize).to_vec() }
}

fn sid_text(sid: PSID) -> String {
    unsafe {
        let mut p = PWSTR::null();
        match ConvertSidToStringSidW(sid, &mut p) {
            Ok(()) => {
                let s = p.to_string().unwrap_or_default();
                let _ = LocalFree(Some(HLOCAL(p.0 as *mut c_void)));
                s
            }
            Err(_) => "an unknown account".into(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use windows::Win32::Security::Authorization::ConvertStringSidToSidW;

    /// A SID from its text form; the bytes live as long as the Vec.
    fn sid(text: &str) -> Vec<u8> {
        let wide: Vec<u16> = text.encode_utf16().chain(Some(0)).collect();
        let mut p = PSID::default();
        unsafe { ConvertStringSidToSidW(PCWSTR(wide.as_ptr()), &mut p) }.expect("a valid SID");
        let bytes = sid_bytes(p);
        unsafe {
            let _ = LocalFree(Some(HLOCAL(p.0)));
        }
        bytes
    }

    fn trusted(owner: &str, caller: &str) -> bool {
        let (o, c) = (sid(owner), sid(caller));
        owner_is_trusted(PSID(o.as_ptr() as *mut c_void), PSID(c.as_ptr() as *mut c_void))
    }

    const ME: &str = "S-1-5-21-1-2-3-1001";

    #[test]
    fn system_administrators_and_the_caller_own_a_trusted_pipe() {
        assert!(trusted("S-1-5-18", ME), "SYSTEM");
        assert!(trusted("S-1-5-32-544", ME), "Administrators");
        assert!(trusted(ME, ME), "the calling user");
    }

    #[test]
    fn anyone_else_does_not() {
        assert!(!trusted("S-1-5-21-1-2-3-1002", ME), "another user");
        assert!(!trusted("S-1-1-0", ME), "Everyone");
        assert!(!trusted("S-1-5-19", ME), "Local Service");
    }

    #[test]
    fn the_callers_own_sid_can_be_read() {
        let me = caller_sid().unwrap();
        assert!(me.len() >= 8 && me[0] == 1, "a SID starts with revision 1");
    }

    #[test]
    fn a_missing_pipe_is_not_found_rather_than_refused() {
        let e = open(r"\\.\pipe\plaitway-no-such-pipe-xyz").err().unwrap();
        assert_eq!(e.kind(), io::ErrorKind::NotFound, "{e}");
    }
}

