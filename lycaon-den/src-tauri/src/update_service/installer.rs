//! Platform installation effects; candidate platforms never enter activation.
use super::{staging, verification, Candidate, Failure, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};
#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
pub use macos::{acquire_lease, exchange, InstallationLease};

pub fn bundle() -> Result<PathBuf, UpdateError> {
    let exe = std::env::current_exe()
        .map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    let root = exe
        .parent()
        .and_then(Path::parent)
        .and_then(Path::parent)
        .ok_or(Failure::UnsupportedInstallation)?;
    if root.extension().and_then(|s| s.to_str()) != Some("app") {
        return Err(Failure::UnsupportedInstallation.into());
    }
    fs::canonicalize(root).map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))
}
pub fn supported() -> bool {
    cfg!(target_os = "macos") && bundle().is_ok()
}
pub fn prepared_path(target: &Path, candidate: &Candidate) -> Result<PathBuf, UpdateError> {
    if !candidate.valid_identity() {
        return Err(Failure::InvalidRelease.into());
    }
    Ok(target
        .parent()
        .ok_or(Failure::UnsupportedInstallation)?
        .join(format!(
            "{}{}.app",
            prepared_prefix(target),
            candidate.release_id
        )))
}
pub fn prepared_prefix(target: &Path) -> String {
    use sha2::{Digest, Sha256};
    format!(
        ".paintedwolf-update-{:x}-",
        Sha256::digest(target.as_os_str().as_encoded_bytes())
    )
}
pub fn cleanup_prepared(target: &Path, keep: &[String]) -> Result<(), UpdateError> {
    let parent = target.parent().ok_or(Failure::UnsupportedInstallation)?;
    let prefix = prepared_prefix(target);
    for entry in fs::read_dir(parent).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))? {
        let entry = entry.map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        let Some(id) = name
            .strip_prefix(&prefix)
            .and_then(|name| name.strip_suffix(".app"))
        else {
            continue;
        };
        if id.len() == 64
            && id.bytes().all(|b| b.is_ascii_hexdigit())
            && !keep.iter().any(|kept| kept == id)
            && entry.file_type().is_ok_and(|kind| kind.is_dir())
        {
            fs::remove_dir_all(entry.path())
                .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
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
pub fn prepare(candidate: &Candidate) -> Result<(), UpdateError> {
    prepare_at(&bundle()?, candidate)
}
pub fn prepare_at(target: &Path, candidate: &Candidate) -> Result<(), UpdateError> {
    let artifact = staging::root(candidate)?.join("artifact");
    verification::verify(
        &artifact,
        &candidate.artifact_signature,
        &super::check::embedded_key().1,
        &candidate.version,
    )
    .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
    #[cfg(target_os = "macos")]
    {
        macos::prepare(target, candidate, &artifact)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = target;
        Err(Failure::UnsupportedInstallation.into())
    }
}
pub fn verify_bundle(path: &Path) -> Result<(), UpdateError> {
    #[cfg(target_os = "macos")]
    {
        let output = std::process::Command::new("/usr/bin/codesign")
            .args([
                "--verify",
                "--deep",
                "--strict",
                "-R",
                "identifier \"dev.paintedwolf.code\"",
            ])
            .arg(path)
            .output()
            .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
        if !output.status.success() {
            return Err(UpdateError::new(
                Failure::VerificationFailed,
                String::from_utf8_lossy(&output.stderr),
            ));
        }
        Ok(())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = path;
        Err(Failure::UnsupportedInstallation.into())
    }
}
