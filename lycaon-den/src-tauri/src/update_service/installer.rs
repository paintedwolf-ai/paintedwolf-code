//! Platform preparation, installation identity, and activation effects.
use super::{staging, verification, Candidate, Failure, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};
#[cfg(unix)]
pub(super) mod archive;
mod identity;
pub(super) mod layout;
pub(super) use identity::{bundle_hash, executable, hash};
use layout::Layout;
pub struct PreparedIdentity {
    pub executable_hash: String,
    pub bundle_hash: String,
}
#[cfg(any(target_os = "macos", target_os = "linux"))]
mod coordination;
#[cfg(target_os = "linux")]
mod linux;
#[cfg(target_os = "macos")]
mod macos;
#[cfg(any(target_os = "macos", target_os = "linux"))]
pub use coordination::{
    acquire_gate, acquire_lease, acquire_preparation, downgrade, exchange_coordinated,
    try_activation, InstallationLease,
};

#[cfg(target_os = "linux")]
use linux as platform;
#[cfg(target_os = "macos")]
use macos as platform;
#[cfg(not(any(target_os = "macos", target_os = "linux")))]
mod unsupported;
#[cfg(target_os = "macos")]
pub use macos::PRODUCT_VERSION_RESOURCE;
pub use platform::{bundle, product_version, supported, verify_bundle};
#[cfg(not(any(target_os = "macos", target_os = "linux")))]
use unsupported as platform;
pub fn prepared_path(target: &Path, candidate: &Candidate) -> Result<PathBuf, UpdateError> {
    if !candidate.valid_identity() {
        return Err(Failure::InvalidRelease.into());
    }
    let extension = Layout::for_platform(&candidate.platform)?.extension();
    Ok(target
        .parent()
        .ok_or(Failure::UnsupportedInstallation)?
        .join(format!(
            "{}{}.{extension}",
            prepared_prefix(target),
            candidate.release_id
        )))
}
pub fn prepared_prefix(target: &Path) -> String {
    use sha2::{Digest, Sha256};
    format!(
        ".paintedwolf-update-{}-uid-{}-",
        hex::encode(Sha256::digest(target.as_os_str().as_encoded_bytes())),
        current_uid()
    )
}
pub fn current_uid() -> u32 {
    #[cfg(unix)]
    {
        unsafe { libc::geteuid() }
    }
    #[cfg(not(unix))]
    {
        0
    }
}
pub fn legacy_prepared_path(target: &Path, candidate: &Candidate) -> Result<PathBuf, UpdateError> {
    if Layout::for_platform(&candidate.platform)? != Layout::AppBundle {
        return Err(Failure::InvalidRelease.into());
    }
    Ok(target
        .parent()
        .ok_or(Failure::InvalidRelease)?
        .join(format!(
            ".paintedwolf-update-{}-{}.app",
            super::persistence::installation_id(target),
            candidate.release_id
        )))
}
pub fn valid_prepared_path(target: &Path, candidate: &Candidate, path: &Path) -> bool {
    candidate.valid_identity()
        && (prepared_path(target, candidate).is_ok_and(|p| p == path)
            || legacy_prepared_path(target, candidate).is_ok_and(|p| p == path))
}
pub fn current_user_installation(path: &Path) -> bool {
    fs::symlink_metadata(path).is_ok_and(|metadata| {
        #[cfg(unix)]
        {
            use std::os::unix::fs::MetadataExt;
            Layout::native().is_ok_and(|layout| layout.accepts(&metadata))
                && metadata.uid() == current_uid()
        }
        #[cfg(not(unix))]
        {
            Layout::native().is_ok_and(|layout| layout.accepts(&metadata))
        }
    })
}
pub fn remove_installation(path: &Path) -> Result<(), UpdateError> {
    let result = Layout::native()?.remove(path);
    match result {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(UpdateError::new(Failure::StateUnavailable, e)),
    }
}
pub fn cleanup_prepared(target: &Path, keep: &[String]) -> Result<(), UpdateError> {
    let parent = target.parent().ok_or(Failure::UnsupportedInstallation)?;
    let prefix = prepared_prefix(target);
    let suffix = format!(".{}", Layout::native()?.extension());
    for entry in fs::read_dir(parent).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))? {
        let entry = entry.map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        let Some(id) = name
            .strip_prefix(&prefix)
            .and_then(|name| name.strip_suffix(&suffix))
        else {
            continue;
        };
        if super::persistence::hex_digest(id)
            && !keep.iter().any(|kept| kept == id)
            && current_user_installation(&entry.path())
        {
            remove_installation(&entry.path())?;
        }
    }
    Ok(())
}
pub fn check_space(path: &Path, required: u64) -> Result<(), UpdateError> {
    #[cfg(unix)]
    {
        use std::{ffi::CString, os::unix::ffi::OsStrExt};
        let path = CString::new(path.as_os_str().as_bytes())
            .map_err(|e| UpdateError::new(Failure::DiskSpace, e))?;
        let mut stat = std::mem::MaybeUninit::<libc::statvfs>::uninit();
        if unsafe { libc::statvfs(path.as_ptr(), stat.as_mut_ptr()) } != 0 {
            return Err(UpdateError::new(
                Failure::DiskSpace,
                std::io::Error::last_os_error(),
            ));
        }
        let stat = unsafe { stat.assume_init() };
        if (stat.f_bavail as u64).saturating_mul(stat.f_frsize as u64)
            < required.saturating_add(verification::limits().disk_reserve_bytes)
        {
            return Err(Failure::DiskSpace.into());
        }
    }
    #[cfg(not(unix))]
    {
        let _ = (path, required);
        return Err(Failure::UnsupportedInstallation.into());
    }
    #[allow(unreachable_code)]
    Ok(())
}
pub fn probe_destination() -> Result<(), UpdateError> {
    let target = bundle()?;
    let parent = target.parent().ok_or(Failure::UnsupportedInstallation)?;
    let probe = parent.join(format!(
        "{}probe-{}",
        prepared_prefix(&target),
        uuid::Uuid::new_v4()
    ));
    fs::create_dir(&probe).map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    fs::remove_dir(probe).map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))
}
pub fn prepare(candidate: &Candidate) -> Result<PreparedIdentity, UpdateError> {
    prepare_at(&bundle()?, candidate)
}
pub fn prepare_at(target: &Path, candidate: &Candidate) -> Result<PreparedIdentity, UpdateError> {
    let artifact = staging::root(candidate)?.join("artifact");
    let mut archive =
        fs::File::open(&artifact).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    let verified = verification::verify_file(
        &mut archive,
        &candidate.artifact_signature,
        &super::feed::embedded_key().1,
        &candidate.version,
    )
    .map_err(|e| UpdateError::new(Failure::VerificationFailed, e));
    retain_verified_artifact(&artifact, verified)?;
    platform::prepare(target, candidate, &mut archive)
}
// Failed verification evicts the cached archive.
fn retain_verified_artifact(
    path: &Path,
    verified: Result<(), UpdateError>,
) -> Result<(), UpdateError> {
    if let Err(error) = verified {
        match fs::remove_file(path) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => {
                return Err(error.with_context(format!("Could not remove rejected archive: {e}")))
            }
        }
        return Err(error);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn only_failed_archive_verification_evicts_the_cached_artifact() {
        let root = crate::test_support::TempDir::new("update-cache-verification");
        let path = root.join("artifact");
        fs::write(&path, b"archive").unwrap();
        retain_verified_artifact(&path, Ok(())).unwrap();
        assert_eq!(fs::read(&path).unwrap(), b"archive");
        let error =
            retain_verified_artifact(&path, Err(Failure::VerificationFailed.into())).unwrap_err();
        assert_eq!(error.code, Failure::VerificationFailed);
        assert!(!path.exists());
    }
}
