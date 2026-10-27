//! What to tell the user about the helper, and what to offer, from what the app knows of it. The words
//! are the ones of the shared catalogs where they exist, so they read the same as in the WinUI app.

use plaitway_core::tr;
use plaitway_helper::{HelperState, Status, Verdict};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Command {
    Install,
    Start,
    Reinstall,
    Uninstall,
}

/// Lines to show, and the buttons that make sense.
#[derive(PartialEq, Eq, Debug)]
pub struct Offer {
    /// The headline: what is wrong, or that all is well.
    pub title: String,
    pub lines: Vec<String>,
    pub commands: Vec<(Command, String)>,
}

/// Fills `%@` and `%lld` of a catalog text, one argument for each, in order.
pub fn printf(key: &str, args: &[&str]) -> String {
    let mut text = tr(key).into_owned();
    for a in args {
        let at = [text.find("%lld"), text.find("%@")].into_iter().flatten().min();
        if let Some(i) = at {
            let len = if text[i..].starts_with("%lld") { 4 } else { 2 };
            text.replace_range(i..i + len, a);
        }
    }
    text
}

fn distrust_reason(verdict: Verdict, path: &str) -> String {
    let key = match verdict {
        Verdict::FolderWritableByUsers => "The helper file is in a folder that standard users can change: %@",
        Verdict::FolderNotLocal => "The helper file is not on a local disk: %@",
        Verdict::SignatureInvalid => "The signature of the helper file is not valid: %@",
        Verdict::SignatureOtherPublisher => "The helper file is signed by an unexpected publisher: %@",
        _ => "The folder of the helper file could not be checked: %@",
    };
    printf(key, &[path])
}

/// `daemon_reachable`: the app is talking to the daemon right now.
pub fn offer(status: &Status, daemon_reachable: bool) -> Offer {
    if let Some(d) = &status.distrust {
        return Offer {
            title: tr("Helper not trusted").into_owned(),
            lines: vec![distrust_reason(d.verdict, &d.path.to_string_lossy()), tr("Install Plaitway again in a folder that only administrators can change.").into_owned()],
            commands: vec![],
        };
    }
    let explain = || tr("The helper manages routes and DNS with administrator rights. Windows asks for confirmation once.").into_owned();
    if status.executable.is_none() {
        return Offer { title: tr("Helper not found").into_owned(), lines: vec![tr("The helper file is missing from this copy of Plaitway. Install Plaitway again.").into_owned()], commands: vec![] };
    }
    let cmd = |c, key: &str| (c, tr(key).into_owned());
    match status.state {
        HelperState::NotInstalled => Offer { title: tr("Helper not installed").into_owned(), lines: vec![explain()], commands: vec![cmd(Command::Install, "Install the helper")] },
        HelperState::Stopped => Offer { title: tr("Helper stopped").into_owned(), lines: vec![explain()], commands: vec![cmd(Command::Start, "Start Helper"), cmd(Command::Uninstall, "Uninstall the helper")] },
        HelperState::Starting => Offer { title: tr("Starting").into_owned(), lines: vec![], commands: vec![] },
        HelperState::Stopping => Offer { title: tr("Stopping").into_owned(), lines: vec![], commands: vec![] },
        HelperState::Running if daemon_reachable => Offer { title: tr("Running").into_owned(), lines: vec![], commands: vec![cmd(Command::Reinstall, "Update the helper"), cmd(Command::Uninstall, "Uninstall the helper")] },
        HelperState::Running => Offer { title: tr("Running, not answering.").into_owned(), lines: vec![tr("Start the helper again from this version.").into_owned()], commands: vec![cmd(Command::Reinstall, "Update the helper")] },
        HelperState::Unknown => Offer { title: tr("Helper state unknown").into_owned(), lines: vec![], commands: vec![] },
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use plaitway_helper::Distrust;
    use std::path::PathBuf;

    fn status(state: HelperState) -> Status {
        Status { state, executable: Some(PathBuf::from(r"C:\Program Files\Plaitway\plaitwayd.exe")), distrust: None }
    }

    fn labels(o: &Offer) -> Vec<Command> {
        o.commands.iter().map(|c| c.0).collect()
    }

    #[test]
    fn a_helper_that_is_not_installed_offers_to_install_it_and_says_why_windows_asks() {
        let o = offer(&status(HelperState::NotInstalled), false);
        assert_eq!(labels(&o), [Command::Install]);
        assert!(o.lines[0].contains("administrator rights"));
    }

    #[test]
    fn a_stopped_helper_offers_to_start_it_or_remove_it() {
        assert_eq!(labels(&offer(&status(HelperState::Stopped), false)), [Command::Start, Command::Uninstall]);
    }

    #[test]
    fn a_running_helper_that_answers_can_be_updated_or_removed_and_one_that_does_not_can_be_started_again() {
        assert_eq!(labels(&offer(&status(HelperState::Running), true)), [Command::Reinstall, Command::Uninstall]);
        let o = offer(&status(HelperState::Running), false);
        assert_eq!(labels(&o), [Command::Reinstall]);
        assert_eq!(o.title, "Running, not answering.");
    }

    #[test]
    fn a_starting_or_stopping_helper_offers_nothing_to_press() {
        assert!(offer(&status(HelperState::Starting), false).commands.is_empty());
        assert!(offer(&status(HelperState::Stopping), false).commands.is_empty());
    }

    #[test]
    fn a_refused_program_offers_nothing_and_names_the_file_and_the_way_out() {
        let s = Status { state: HelperState::NotInstalled, executable: None, distrust: Some(Distrust { verdict: Verdict::FolderWritableByUsers, path: PathBuf::from(r"C:\Users\me\Downloads\plaitwayd.exe") }) };
        let o = offer(&s, false);
        assert!(o.commands.is_empty());
        assert_eq!(o.lines[0], r"The helper file is in a folder that standard users can change: C:\Users\me\Downloads\plaitwayd.exe");
        assert!(o.lines[1].contains("only administrators"));
        assert_eq!(o.title, "Helper not trusted");
    }

    #[test]
    fn a_missing_program_says_to_install_plaitway_again() {
        let s = Status { state: HelperState::NotInstalled, executable: None, distrust: None };
        let o = offer(&s, false);
        assert!(o.commands.is_empty());
        assert!(o.lines[0].contains("Install Plaitway again"));
    }

    #[test]
    fn printf_fills_each_placeholder_once_in_order() {
        assert_eq!(printf("Helper command failed with exit code %lld", &["5"]), "Helper command failed with exit code 5");
        assert_eq!(printf("A %@ and a %@", &["x", "y"]), "A x and a y");
        assert_eq!(printf("no placeholder", &["x"]), "no placeholder");
    }
}
