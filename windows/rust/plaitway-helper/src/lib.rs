//! The helper service of Windows as the app manages it: where it stands in the Service Control Manager,
//! whether the program that would be registered may be run with administrator rights, and the four
//! commands of `plaitwayd.exe` that change it. Each command is elevated, so each starts with the consent
//! prompt of Windows, which the user answers. Nothing here runs without being asked.
//!
//! The rules are those of the WinUI app's `Plaitway.AppCore/Helper` (removed from the repository), word for word where
//! it matters: a program is run elevated only when only administrators can change the folder it is in,
//! or when it carries a valid signature of Plaitway.

mod win;

pub use win::{ShellElevatedLauncher, WindowsHelperTrust, WindowsService};

use std::path::{Path, PathBuf};

pub const SERVICE_NAME: &str = "PlaitwayHelper";
pub const EXECUTABLE_NAME: &str = "plaitwayd.exe";
/// The publisher whose signature is accepted wherever the file is.
pub const EXPECTED_PUBLISHER: &str = "Plaitway";

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum HelperState {
    NotInstalled,
    Stopped,
    Starting,
    Running,
    Stopping,
    /// The Service Control Manager could not be asked, or said something unknown.
    Unknown,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum FolderProtection {
    /// On a local fixed disk, and no folder of the path can be changed by a standard user.
    AdminOnly,
    WritableByUsers,
    /// A network share, a removable disk, a disk Windows does not know to be fixed.
    NotLocalFixedDisk,
    /// The access lists could not be read.
    Unknown,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum FileSignature {
    /// Signed, the signature checks out, and the publisher is the expected one.
    Valid,
    NotSigned,
    /// A signature that does not check out: changed file, untrusted, expired or revoked certificate.
    Invalid,
    OtherPublisher,
}

/// What the app asks Windows before it lets a program run elevated. Both answers are about the file as it
/// is now, so the caller asks again at the moment it launches. Neither fails: what cannot be found out is
/// reported as `Unknown` or `Invalid`, which are refusals.
pub trait HelperTrust {
    fn inspect_folder(&self, executable: &Path) -> FolderProtection;
    fn inspect_signature(&self, executable: &Path, expected_publisher: &str) -> FileSignature;
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Verdict {
    Trusted,
    /// Unsigned, and a standard user can replace it.
    FolderWritableByUsers,
    FolderNotLocal,
    FolderNotChecked,
    SignatureInvalid,
    SignatureOtherPublisher,
}

/// A program that was found and refused.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Distrust {
    pub verdict: Verdict,
    pub path: PathBuf,
}

/// Trusted when the folder is admin-only, or when the signature is valid. A signature in a folder that
/// users can change is checked at one moment and the file launched at another: the folder is the stronger.
pub fn judge(executable: &Path, trust: &dyn HelperTrust) -> Verdict {
    let folder = trust.inspect_folder(executable);
    if folder == FolderProtection::AdminOnly {
        return Verdict::Trusted;
    }
    match trust.inspect_signature(executable, EXPECTED_PUBLISHER) {
        FileSignature::Valid => Verdict::Trusted,
        FileSignature::Invalid => Verdict::SignatureInvalid,
        FileSignature::OtherPublisher => Verdict::SignatureOtherPublisher,
        FileSignature::NotSigned => match folder {
            FolderProtection::WritableByUsers => Verdict::FolderWritableByUsers,
            FolderProtection::NotLocalFixedDisk => Verdict::FolderNotLocal,
            _ => Verdict::FolderNotChecked,
        },
    }
}

#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Candidate {
    pub path: PathBuf,
    /// The one `go build` made in the repository: the developer's own, in a folder the developer owns.
    pub development: bool,
}

#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct Resolution {
    /// The program that may be run elevated.
    pub trusted: Option<PathBuf>,
    /// The program that was found and refused.
    pub distrust: Option<Distrust>,
}

/// Finds `plaitwayd.exe` from where the app runs: next to the app, or one folder up when the app has a
/// folder of its own; a development build also finds the one `go build -o bin\plaitwayd.exe` made.
pub struct Locator<'a> {
    pub app_dir: PathBuf,
    pub file_exists: &'a dyn Fn(&Path) -> bool,
    pub trust: &'a dyn HelperTrust,
    /// Only a debug build looks in the repository it was built from.
    pub searches_repository: bool,
}

const MAX_DEVELOPMENT_SEARCH_DEPTH: usize = 8;

impl Locator<'_> {
    /// Asked again at every use: the file may have changed.
    pub fn resolve(&self) -> Resolution {
        let Some(found) = self.candidates().into_iter().find(|c| (self.file_exists)(&c.path)) else {
            return Resolution::default();
        };
        // The developer's own build lies in a folder the developer can write to, by nature.
        let verdict = if found.development { Verdict::Trusted } else { judge(&found.path, self.trust) };
        if verdict == Verdict::Trusted {
            Resolution { trusted: Some(found.path), distrust: None }
        } else {
            Resolution { trusted: None, distrust: Some(Distrust { verdict, path: found.path }) }
        }
    }

    /// Where the program may be, the most likely first.
    pub fn candidates(&self) -> Vec<Candidate> {
        let mut out = vec![Candidate { path: self.app_dir.join(EXECUTABLE_NAME), development: false }];
        let parent = self.app_dir.parent().map(Path::to_path_buf);
        if let Some(p) = &parent {
            out.push(Candidate { path: p.join(EXECUTABLE_NAME), development: false });
        }
        if self.searches_repository {
            let mut current = parent;
            for _ in 0..MAX_DEVELOPMENT_SEARCH_DEPTH {
                let Some(dir) = current else { break };
                if (self.file_exists)(&dir.join("go.mod")) {
                    out.push(Candidate { path: dir.join("bin").join(EXECUTABLE_NAME), development: true });
                }
                current = dir.parent().map(Path::to_path_buf);
            }
        }
        out
    }
}

pub trait HelperService {
    fn query(&self) -> HelperState;
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum ElevatedOutcome {
    Completed,
    /// The user said no to the consent prompt, so nothing ran.
    Declined,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct ElevatedResult {
    pub outcome: ElevatedOutcome,
    pub exit_code: i32,
}

/// Runs a program elevated, which makes Windows ask the user for consent. The prompt is the user's:
/// nothing else may answer it.
pub trait ElevatedLauncher {
    fn run(&self, executable: &Path, arguments: &str) -> std::io::Result<ElevatedResult>;
}

/// What the app knows about the helper without asking the daemon.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Status {
    pub state: HelperState,
    /// Where `plaitwayd.exe` is and may be run elevated.
    pub executable: Option<PathBuf>,
    pub distrust: Option<Distrust>,
}

#[derive(Debug)]
pub enum Error {
    /// No program to register or ask, or one that was refused.
    Missing(Option<Distrust>),
    /// The command ran and ended with this exit code; its reason went to a console nobody saw.
    CommandFailed(i32),
    /// The program could not be started for a reason other than a refusal of the prompt.
    Launch(std::io::Error),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Missing(None) => write!(f, "{EXECUTABLE_NAME} was not found next to the app"),
            Error::Missing(Some(d)) => write!(f, "{EXECUTABLE_NAME} at {} is not trusted: {:?}", d.path.display(), d.verdict),
            Error::CommandFailed(c) => write!(f, "{EXECUTABLE_NAME} ended with exit code {c}"),
            Error::Launch(e) => write!(f, "could not start {EXECUTABLE_NAME}: {e}"),
        }
    }
}

impl std::error::Error for Error {}

const INSTALL: &str = "install -start";
const START: &str = "start";
const UPDATE: &str = "install -update -start";
const UNINSTALL: &str = "uninstall";

pub struct Installer<'a> {
    pub service: &'a dyn HelperService,
    pub launcher: &'a dyn ElevatedLauncher,
    pub locator: Locator<'a>,
}

impl Installer<'_> {
    pub fn status(&self) -> Status {
        let r = self.locator.resolve();
        Status { state: self.service.query(), executable: r.trusted, distrust: r.distrust }
    }

    /// Registers the service and starts it. `Ok(false)` when the user declined the consent prompt.
    pub fn install(&self) -> Result<bool, Error> {
        self.run(INSTALL)
    }

    pub fn start(&self) -> Result<bool, Error> {
        self.run(START)
    }

    /// Replaces the registration with the one of this copy of the program, which restarts the service and
    /// drops every tunnel.
    pub fn reinstall(&self) -> Result<bool, Error> {
        self.run(UPDATE)
    }

    /// Stops the service and removes it; the profiles stay on disk.
    pub fn uninstall(&self) -> Result<bool, Error> {
        self.run(UNINSTALL)
    }

    fn run(&self, arguments: &str) -> Result<bool, Error> {
        // Judged again here, not taken from an earlier status: the file may have been replaced since.
        let resolution = self.locator.resolve();
        let Some(program) = resolution.trusted else {
            return Err(Error::Missing(resolution.distrust));
        };
        let result = self.launcher.run(&program, arguments).map_err(Error::Launch)?;
        match (result.outcome, result.exit_code) {
            (ElevatedOutcome::Declined, _) => Ok(false),
            (_, 0) => Ok(true),
            (_, code) => Err(Error::CommandFailed(code)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;

    struct FakeTrust {
        folder: FolderProtection,
        signature: FileSignature,
    }

    impl HelperTrust for FakeTrust {
        fn inspect_folder(&self, _: &Path) -> FolderProtection {
            self.folder
        }
        fn inspect_signature(&self, _: &Path, publisher: &str) -> FileSignature {
            assert_eq!(publisher, EXPECTED_PUBLISHER);
            self.signature
        }
    }

    fn trust(folder: FolderProtection, signature: FileSignature) -> FakeTrust {
        FakeTrust { folder, signature }
    }

    #[test]
    fn an_admin_only_folder_is_enough_whatever_the_signature() {
        for s in [FileSignature::Valid, FileSignature::NotSigned, FileSignature::Invalid, FileSignature::OtherPublisher] {
            assert_eq!(judge(Path::new("x"), &trust(FolderProtection::AdminOnly, s)), Verdict::Trusted, "{s:?}");
        }
    }

    #[test]
    fn a_valid_signature_is_enough_wherever_the_file_is() {
        for f in [FolderProtection::WritableByUsers, FolderProtection::NotLocalFixedDisk, FolderProtection::Unknown] {
            assert_eq!(judge(Path::new("x"), &trust(f, FileSignature::Valid)), Verdict::Trusted, "{f:?}");
        }
    }

    #[test]
    fn an_unsigned_file_is_refused_for_the_reason_of_its_folder() {
        let v = |f| judge(Path::new("x"), &trust(f, FileSignature::NotSigned));
        assert_eq!(v(FolderProtection::WritableByUsers), Verdict::FolderWritableByUsers);
        assert_eq!(v(FolderProtection::NotLocalFixedDisk), Verdict::FolderNotLocal);
        assert_eq!(v(FolderProtection::Unknown), Verdict::FolderNotChecked);
    }

    #[test]
    fn a_bad_signature_is_refused_whatever_the_folder() {
        for f in [FolderProtection::WritableByUsers, FolderProtection::NotLocalFixedDisk, FolderProtection::Unknown] {
            assert_eq!(judge(Path::new("x"), &trust(f, FileSignature::Invalid)), Verdict::SignatureInvalid);
            assert_eq!(judge(Path::new("x"), &trust(f, FileSignature::OtherPublisher)), Verdict::SignatureOtherPublisher);
        }
    }

    fn locator<'a>(app: &str, files: &'a dyn Fn(&Path) -> bool, t: &'a FakeTrust, repo: bool) -> Locator<'a> {
        Locator { app_dir: PathBuf::from(app), file_exists: files, trust: t, searches_repository: repo }
    }

    #[test]
    fn the_program_next_to_the_app_is_found_first_then_the_one_a_folder_up() {
        let t = trust(FolderProtection::AdminOnly, FileSignature::NotSigned);
        let both = |p: &Path| p.ends_with("plaitwayd.exe");
        assert_eq!(locator(r"C:\Program Files\Plaitway\app", &both, &t, false).resolve().trusted, Some(PathBuf::from(r"C:\Program Files\Plaitway\app\plaitwayd.exe")));
        let up_only = |p: &Path| p == Path::new(r"C:\Program Files\Plaitway\plaitwayd.exe");
        assert_eq!(locator(r"C:\Program Files\Plaitway\app", &up_only, &t, false).resolve().trusted, Some(PathBuf::from(r"C:\Program Files\Plaitway\plaitwayd.exe")));
    }

    #[test]
    fn nothing_found_is_not_a_refusal() {
        let t = trust(FolderProtection::AdminOnly, FileSignature::Valid);
        let none = |_: &Path| false;
        assert_eq!(locator(r"C:\x\app", &none, &t, true).resolve(), Resolution::default());
    }

    #[test]
    fn a_program_in_a_folder_users_can_change_is_found_and_refused_with_its_path() {
        let t = trust(FolderProtection::WritableByUsers, FileSignature::NotSigned);
        let any = |p: &Path| p.ends_with("plaitwayd.exe");
        let r = locator(r"C:\Users\me\Downloads\Plaitway", &any, &t, false).resolve();
        assert_eq!(r.trusted, None);
        assert_eq!(r.distrust, Some(Distrust { verdict: Verdict::FolderWritableByUsers, path: PathBuf::from(r"C:\Users\me\Downloads\Plaitway\plaitwayd.exe") }));
    }

    #[test]
    fn the_development_build_is_found_only_when_asked_and_is_not_judged() {
        let t = trust(FolderProtection::WritableByUsers, FileSignature::NotSigned);
        let repo = |p: &Path| p == Path::new(r"C:\src\plaitway\go.mod") || p == Path::new(r"C:\src\plaitway\bin\plaitwayd.exe");
        let app = r"C:\src\plaitway\windows\build\app";
        assert_eq!(locator(app, &repo, &t, true).resolve().trusted, Some(PathBuf::from(r"C:\src\plaitway\bin\plaitwayd.exe")));
        assert_eq!(locator(app, &repo, &t, false).resolve(), Resolution::default(), "a release build never looks in the repository");
    }

    struct FakeService(HelperState);
    impl HelperService for FakeService {
        fn query(&self) -> HelperState {
            self.0
        }
    }

    struct FakeLauncher {
        result: std::io::Result<ElevatedResult>,
        calls: RefCell<Vec<(PathBuf, String)>>,
    }
    impl ElevatedLauncher for FakeLauncher {
        fn run(&self, p: &Path, a: &str) -> std::io::Result<ElevatedResult> {
            self.calls.borrow_mut().push((p.to_path_buf(), a.to_string()));
            match &self.result {
                Ok(r) => Ok(*r),
                Err(e) => Err(std::io::Error::new(e.kind(), e.to_string())),
            }
        }
    }

    fn launcher(outcome: ElevatedOutcome, code: i32) -> FakeLauncher {
        FakeLauncher { result: Ok(ElevatedResult { outcome, exit_code: code }), calls: RefCell::new(vec![]) }
    }

    fn installer<'a>(svc: &'a FakeService, l: &'a FakeLauncher, t: &'a FakeTrust, files: &'a dyn Fn(&Path) -> bool) -> Installer<'a> {
        Installer { service: svc, launcher: l, locator: locator(r"C:\Program Files\Plaitway\app", files, t, false) }
    }

    #[test]
    fn each_command_runs_the_trusted_program_with_its_arguments() {
        let (svc, t) = (FakeService(HelperState::Stopped), trust(FolderProtection::AdminOnly, FileSignature::NotSigned));
        let l = launcher(ElevatedOutcome::Completed, 0);
        let files = |p: &Path| p.ends_with("plaitwayd.exe");
        let i = installer(&svc, &l, &t, &files);
        assert!(i.install().unwrap() && i.start().unwrap() && i.reinstall().unwrap() && i.uninstall().unwrap());
        let args: Vec<String> = l.calls.borrow().iter().map(|c| c.1.clone()).collect();
        assert_eq!(args, ["install -start", "start", "install -update -start", "uninstall"]);
        assert!(l.calls.borrow().iter().all(|c| c.0 == Path::new(r"C:\Program Files\Plaitway\app\plaitwayd.exe")));
    }

    #[test]
    fn a_declined_prompt_is_false_and_a_failed_command_is_an_error_with_its_code() {
        let (svc, t) = (FakeService(HelperState::NotInstalled), trust(FolderProtection::AdminOnly, FileSignature::NotSigned));
        let files = |p: &Path| p.ends_with("plaitwayd.exe");
        let declined = launcher(ElevatedOutcome::Declined, 0);
        assert!(!installer(&svc, &declined, &t, &files).install().unwrap());
        let failed = launcher(ElevatedOutcome::Completed, 5);
        assert!(matches!(installer(&svc, &failed, &t, &files).install(), Err(Error::CommandFailed(5))));
    }

    #[test]
    fn nothing_is_launched_when_the_program_is_missing_or_refused() {
        let svc = FakeService(HelperState::NotInstalled);
        let l = launcher(ElevatedOutcome::Completed, 0);
        let (none, any) = (|_: &Path| false, |p: &Path| p.ends_with("plaitwayd.exe"));
        let ok = trust(FolderProtection::AdminOnly, FileSignature::Valid);
        assert!(matches!(installer(&svc, &l, &ok, &none).install(), Err(Error::Missing(None))));
        let bad = trust(FolderProtection::WritableByUsers, FileSignature::NotSigned);
        match installer(&svc, &l, &bad, &any).install() {
            Err(Error::Missing(Some(d))) => assert_eq!(d.verdict, Verdict::FolderWritableByUsers),
            other => panic!("{other:?}"),
        }
        assert!(l.calls.borrow().is_empty(), "the prompt must never be shown for a program that is not trusted");
    }

    #[test]
    fn a_program_that_cannot_be_started_is_a_launch_error() {
        let (svc, t) = (FakeService(HelperState::Stopped), trust(FolderProtection::AdminOnly, FileSignature::NotSigned));
        let l = FakeLauncher { result: Err(std::io::Error::other("no")), calls: RefCell::new(vec![]) };
        let files = |p: &Path| p.ends_with("plaitwayd.exe");
        assert!(matches!(installer(&svc, &l, &t, &files).start(), Err(Error::Launch(_))));
    }

    #[test]
    fn the_status_says_where_the_service_is_and_whether_the_program_may_run() {
        let (svc, t) = (FakeService(HelperState::Running), trust(FolderProtection::AdminOnly, FileSignature::NotSigned));
        let l = launcher(ElevatedOutcome::Completed, 0);
        let files = |p: &Path| p.ends_with("plaitwayd.exe");
        let s = installer(&svc, &l, &t, &files).status();
        assert_eq!(s.state, HelperState::Running);
        assert!(s.executable.is_some() && s.distrust.is_none());
    }
}
