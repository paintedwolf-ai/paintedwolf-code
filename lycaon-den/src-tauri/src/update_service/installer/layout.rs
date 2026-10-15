//! Installation shapes used by preparation receipts and native file identity.
use super::{Failure, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(in crate::update_service) enum Layout {
    AppBundle,
}
impl Layout {
    pub(in crate::update_service) fn for_platform(platform: &str) -> Result<Self, UpdateError> {
        match platform {
            "darwin-aarch64" | "darwin-x86_64" => Ok(Self::AppBundle),
            _ => Err(Failure::UnsupportedInstallation.into()),
        }
    }
    pub(in crate::update_service) fn native() -> Result<Self, UpdateError> {
        Self::for_platform(super::super::feed::target())
    }
    pub(in crate::update_service) fn extension(self) -> &'static str {
        match self {
            Self::AppBundle => "app",
        }
    }
    pub(in crate::update_service) fn accepts(self, metadata: &fs::Metadata) -> bool {
        match self {
            Self::AppBundle => metadata.is_dir(),
        }
    }
    pub(in crate::update_service) fn executable(self, root: &Path) -> PathBuf {
        match self {
            Self::AppBundle => root.join("Contents/MacOS/painted-wolf-code"),
        }
    }
    pub(in crate::update_service) fn remove(self, root: &Path) -> std::io::Result<()> {
        match self {
            Self::AppBundle => fs::remove_dir_all(root),
        }
    }
    pub(in crate::update_service) fn matches_path(self, path: &Path) -> bool {
        path.is_absolute() && path.extension().and_then(|s| s.to_str()) == Some(self.extension())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn platform_shapes_are_closed_and_do_not_default_to_macos() {
        for platform in ["darwin-aarch64", "darwin-x86_64"] {
            assert_eq!(Layout::for_platform(platform).unwrap(), Layout::AppBundle);
        }
        // Package-managed Linux has no native shape to prepare or exchange.
        for platform in [
            "linux-aarch64",
            "linux-x86_64",
            "windows-x86_64",
            "linux-unknown",
            "freebsd-x86_64",
        ] {
            assert!(Layout::for_platform(platform).is_err());
        }
        assert_eq!(
            Layout::AppBundle.executable(Path::new("App.app")),
            Path::new("App.app/Contents/MacOS/painted-wolf-code")
        );
    }
}
