//! Platform installation effects; candidate platforms never enter activation.
use super::{staging, verification, Candidate, Failure, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};
mod identity;
#[cfg(target_os = "macos")]
pub(super) use identity::bundle_hash;
pub(super) use identity::{executable, hash};
pub struct PreparedIdentity {
    pub executable_hash: String,
    pub bundle_hash: String,
}
#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
pub use macos::{acquire_gate, acquire_lease, exchange, InstallationLease};

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
    #[cfg(target_os = "macos")]
    {
        bundle().is_ok_and(|target| macos::writable_installation(&target))
    }
    #[cfg(not(target_os = "macos"))]
    {
        false
    }
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
    let verified = verification::verify(
        &artifact,
        &candidate.artifact_signature,
        &super::check::embedded_key().1,
        &candidate.version,
    )
    .map_err(|e| UpdateError::new(Failure::VerificationFailed, e));
    retain_verified_artifact(&artifact, verified)?;
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
// Eviction belongs to archive verification, independently of UI state or relaunch.
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
mod artifact_tests {
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

pub fn verify_bundle(path: &Path) -> Result<(), UpdateError> {
    #[cfg(target_os = "macos")]
    {
        let output = std::process::Command::new("/usr/bin/codesign")
            .args([
                "--verify",
                "--deep",
                "--strict",
                "-R",
                &signing_requirement()?,
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

#[cfg(target_os = "macos")]
fn signing_requirement() -> Result<String, UpdateError> {
    static REQUIREMENT: std::sync::LazyLock<Result<String, UpdateError>> =
        std::sync::LazyLock::new(|| {
            let executable = std::env::current_exe()
                .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
            let output = std::process::Command::new("/usr/bin/codesign")
                .args(["--display", "--verbose=4"])
                .arg(executable)
                .output()
                .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
            if !output.status.success() {
                return Err(Failure::VerificationFailed.into());
            }
            let metadata = String::from_utf8_lossy(&output.stderr);
            let team = metadata
                .lines()
                .find_map(|line| line.strip_prefix("TeamIdentifier="));
            publisher_requirement(team, cfg!(debug_assertions))
        });
    REQUIREMENT.clone()
}
#[cfg(target_os = "macos")]
fn publisher_requirement(team: Option<&str>, development: bool) -> Result<String, UpdateError> {
    let identifier = "identifier \"dev.paintedwolf.code\"";
    match team {
        Some(team) if team.len() == 10 && team.bytes().all(|b| b.is_ascii_uppercase() || b.is_ascii_digit()) =>
            Ok(format!("{identifier} and anchor apple generic and certificate leaf[subject.OU] = \"{team}\"")),
        Some("not set") | None if development => Ok(identifier.into()),
        _ => Err(Failure::VerificationFailed.into()),
    }
}
#[cfg(all(test, target_os = "macos"))]
mod signing_tests {
    use super::*;
    #[test]
    fn production_requires_a_concrete_publisher_and_cannot_inject_a_requirement() {
        assert!(publisher_requirement(Some("TEAM123456"), false)
            .unwrap()
            .contains("certificate leaf[subject.OU] = \"TEAM123456\""));
        assert!(publisher_requirement(Some("not set"), false).is_err());
        assert!(publisher_requirement(None, false).is_err());
        assert!(publisher_requirement(Some("TEAM\" or true"), false).is_err());
        assert!(publisher_requirement(Some("not set"), true).is_ok());
    }
}
