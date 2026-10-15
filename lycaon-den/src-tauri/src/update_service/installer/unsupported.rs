//! Native activation is unavailable on targets without an installation adapter.
use super::{Candidate, Failure, PreparedIdentity, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};

pub const CHECK_ONLY: bool = false;
pub fn supported() -> bool {
    false
}
pub fn bundle() -> Result<PathBuf, UpdateError> {
    Err(Failure::UnsupportedInstallation.into())
}
pub fn product_version(_: &Path, _: Failure) -> Result<String, UpdateError> {
    Err(Failure::UnsupportedInstallation.into())
}
pub fn verify_bundle(_: &Path) -> Result<(), UpdateError> {
    Err(Failure::UnsupportedInstallation.into())
}
pub fn prepare(_: &Path, _: &Candidate, _: &mut fs::File) -> Result<PreparedIdentity, UpdateError> {
    Err(Failure::UnsupportedInstallation.into())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn absence_of_an_adapter_is_explicit() {
        assert!(!supported());
        assert_eq!(bundle().unwrap_err().code, Failure::UnsupportedInstallation);
        assert_eq!(
            verify_bundle(Path::new("app.exe")).unwrap_err().code,
            Failure::UnsupportedInstallation
        );
    }
}
