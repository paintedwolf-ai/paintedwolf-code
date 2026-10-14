//! Where this build of the application lives decides who owns its launchers
//! and command-line entry points.

use std::path::{Component, Path, PathBuf};

/// Root of a package-managed Linux installation. The package also owns
/// `/usr/bin/pw`, `/usr/bin/pw-logs`, the desktop entry and icons.
pub(crate) const LINUX_PACKAGE_ROOT: &str = "/opt/painted-wolf-code";

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum InstallationMode {
    /// A distribution package installed the application and its entry points.
    Package { root: PathBuf },
    /// A signed application bundle the person placed themselves.
    Bundle,
    /// A build run from a development or scratch location.
    Developer,
}

impl InstallationMode {
    pub(crate) fn current() -> Self {
        match std::env::current_exe() {
            Ok(executable) => classify(&executable, std::env::consts::OS),
            Err(_) => Self::Developer,
        }
    }

    pub(crate) fn label(&self) -> &'static str {
        match self {
            Self::Package { .. } => "package",
            Self::Bundle => "bundle",
            Self::Developer => "developer",
        }
    }
}

/// A symlinked launcher resolves to the real location before classification,
/// so a developer link into the package root still reports the package.
pub(crate) fn classify(executable: &Path, target_os: &str) -> InstallationMode {
    let resolved = std::fs::canonicalize(executable).unwrap_or_else(|_| executable.to_path_buf());
    match target_os {
        "linux" if resolved.starts_with(LINUX_PACKAGE_ROOT) => InstallationMode::Package {
            root: PathBuf::from(LINUX_PACKAGE_ROOT),
        },
        "macos" if inside_app_bundle(&resolved) => InstallationMode::Bundle,
        _ => InstallationMode::Developer,
    }
}

fn inside_app_bundle(executable: &Path) -> bool {
    executable.components().any(|component| {
        matches!(component, Component::Normal(name) if Path::new(name).extension().is_some_and(|ext| ext == "app"))
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn package_root_identifies_linux_package_installations() {
        let mode = classify(Path::new("/opt/painted-wolf-code/bin/painted-wolf-code"), "linux");
        assert_eq!(mode, InstallationMode::Package { root: PathBuf::from(LINUX_PACKAGE_ROOT) });
        assert_eq!(mode.label(), "package");
        assert_eq!(classify(Path::new("/opt/painted-wolf-code-dev/bin/app"), "linux"), InstallationMode::Developer);
        assert_eq!(classify(Path::new("/home/me/src/target/debug/app"), "linux"), InstallationMode::Developer);
        assert_eq!(classify(Path::new("/opt/painted-wolf-code/bin/app"), "macos"), InstallationMode::Developer);
    }

    #[test]
    fn app_bundles_are_bundles_only_on_macos() {
        let bundle = Path::new("/Applications/Painted Wolf Code.app/Contents/MacOS/app");
        assert_eq!(classify(bundle, "macos"), InstallationMode::Bundle);
        assert_eq!(classify(bundle, "linux"), InstallationMode::Developer);
        assert_eq!(classify(Path::new("/Users/me/target/debug/app"), "macos"), InstallationMode::Developer);
    }

    #[cfg(unix)]
    #[test]
    fn symlinked_launchers_classify_by_their_target() {
        let root = crate::test_support::TempDir::new("installation-mode");
        let target = root.path().join("real-app");
        std::fs::write(&target, b"#!/bin/sh\n").unwrap();
        let link = root.path().join("launcher");
        std::os::unix::fs::symlink(&target, &link).unwrap();
        assert_eq!(classify(&link, "linux"), InstallationMode::Developer);
        assert_eq!(classify(&link, "macos"), classify(&target, "macos"));
    }
}
