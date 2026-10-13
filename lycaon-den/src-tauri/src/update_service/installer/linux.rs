//! Linux installations belong to the package manager: discovery only, no activation.
use super::{Candidate, Failure, PreparedIdentity, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};

/// The feed is read for its version; no artifact is downloaded or staged here.
pub const CHECK_ONLY: bool = true;

fn refusal() -> UpdateError {
    if crate::linux_install::installed().is_some() {
        Failure::PackageManaged.into()
    } else {
        Failure::UnsupportedInstallation.into()
    }
}
pub fn supported() -> bool {
    false
}
pub fn bundle() -> Result<PathBuf, UpdateError> {
    Err(refusal())
}
pub fn product_version(_: &Path, _: Failure) -> Result<String, UpdateError> {
    Err(refusal())
}
pub fn verify_bundle(_: &Path) -> Result<(), UpdateError> {
    Err(refusal())
}
pub fn prepare(_: &Path, _: &Candidate, _: &mut fs::File) -> Result<PreparedIdentity, UpdateError> {
    Err(refusal())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn linux_never_advertises_native_activation() {
        assert!(CHECK_ONLY);
        assert!(!supported());
        let code = bundle().unwrap_err().code;
        assert!(matches!(
            code,
            Failure::PackageManaged | Failure::UnsupportedInstallation
        ));
        assert_eq!(
            verify_bundle(Path::new("/usr/bin/painted-wolf-code"))
                .unwrap_err()
                .code,
            code
        );
        assert_eq!(
            product_version(
                Path::new("/opt/painted-wolf-code"),
                Failure::StateUnavailable
            )
            .unwrap_err()
            .code,
            code
        );
    }
}
