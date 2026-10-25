//! Where the `pw` and `pw-logs` entry points live for each installation mode.

use std::path::{Path, PathBuf};

use crate::installation_mode::InstallationMode;

/// Package installations own `/usr/bin`; the application only reports them.
const PACKAGE_BIN_DIR: &str = "/usr/bin";
/// Bundles and developer builds on macOS link into the shared local prefix.
const MACOS_BIN_DIR: &str = "/usr/local/bin";
/// Other Linux installations stay inside the person's own XDG executable
/// directory and never ask for a writable system directory.
const LINUX_USER_BIN_REL: &str = ".local/bin";

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct InstallLocation {
    pub(super) bin_dir: PathBuf,
    pub(super) managed_by_package: bool,
    pub(super) mode: InstallationMode,
}

pub(super) fn current() -> Result<InstallLocation, String> {
    crate::linux_generation::running()?;
    let mut location = resolve(
        InstallationMode::current(),
        std::env::consts::OS,
        std::env::var_os("HOME").map(PathBuf::from).as_deref(),
    )?;
    if matches!(location.mode, InstallationMode::Nix { .. }) {
        if let Ok(selected) = which::which("pw") {
            if let Some(directory) = selected.parent() {
                location.bin_dir = directory.to_path_buf();
            }
        }
    }
    Ok(location)
}

pub(super) fn resolve(
    mode: InstallationMode,
    target_os: &str,
    home: Option<&Path>,
) -> Result<InstallLocation, String> {
    let (bin_dir, managed_by_package) = match (&mode, target_os) {
        (InstallationMode::Package { .. }, _) => (PathBuf::from(PACKAGE_BIN_DIR), true),
        (InstallationMode::Nix { output, .. }, _) => (output.join("bin"), true),
        (_, "linux") => {
            let home = home.filter(|home| home.is_absolute()).ok_or_else(|| {
                "the home directory is unknown, so there is no user command directory".to_string()
            })?;
            (home.join(LINUX_USER_BIN_REL), false)
        }
        _ => (PathBuf::from(MACOS_BIN_DIR), false),
    };
    Ok(InstallLocation {
        bin_dir,
        managed_by_package,
        mode,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn package_installations_report_the_system_entry_points() {
        let location = resolve(
            InstallationMode::Package {
                root: PathBuf::from("/opt/painted-wolf-code"),
            },
            "linux",
            Some(Path::new("/home/me")),
        )
        .unwrap();
        assert_eq!(location.bin_dir, PathBuf::from("/usr/bin"));
        assert!(location.managed_by_package);
    }

    #[test]
    fn linux_developer_builds_use_the_user_executable_directory() {
        let location = resolve(
            InstallationMode::Developer,
            "linux",
            Some(Path::new("/home/me")),
        )
        .unwrap();
        assert_eq!(location.bin_dir, PathBuf::from("/home/me/.local/bin"));
        assert!(!location.managed_by_package);
        assert!(resolve(InstallationMode::Developer, "linux", None).is_err());
        assert!(resolve(
            InstallationMode::Developer,
            "linux",
            Some(Path::new("relative"))
        )
        .is_err());
    }

    #[test]
    fn nix_entry_points_are_owned_by_the_output_and_never_repaired() {
        let output = PathBuf::from("/custom-store/generation");
        let location = resolve(
            InstallationMode::Nix {
                root: output.join("lib/painted-wolf-code"),
                output: output.clone(),
            },
            "linux",
            None,
        )
        .unwrap();
        assert_eq!(location.bin_dir, output.join("bin"));
        assert!(location.managed_by_package);
        assert_eq!(location.mode.label(), "nix");
    }

    #[test]
    fn macos_keeps_the_shared_local_prefix() {
        for mode in [InstallationMode::Bundle, InstallationMode::Developer] {
            let location = resolve(mode, "macos", Some(Path::new("/Users/me"))).unwrap();
            assert_eq!(location.bin_dir, PathBuf::from("/usr/local/bin"));
            assert!(!location.managed_by_package);
        }
    }
}
