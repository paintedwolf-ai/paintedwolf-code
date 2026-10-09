//! Platform installation effects; candidate platforms never enter activation.
use super::{staging, verification, Candidate, Failure, UpdateError};
use std::{
    fs,
    path::{Path, PathBuf},
};
#[cfg(unix)]
pub(super) mod archive;
mod identity;
pub(super) use identity::{bundle_hash, executable, hash};
pub struct PreparedIdentity {
    pub executable_hash: String,
    pub bundle_hash: String,
}
#[cfg(target_os = "macos")]
mod coordination;
#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
pub use coordination::{
    acquire_gate, acquire_lease, acquire_preparation, downgrade, exchange_coordinated,
    try_activation, InstallationLease,
};

pub const PRODUCT_VERSION_RESOURCE: &str = "Contents/Resources/release-version";
const PRODUCT_VERSION_LIMIT: u64 = 128;

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
pub fn product_version(bundle: &Path, failure: Failure) -> Result<String, UpdateError> {
    use std::io::Read;
    let file = fs::File::open(bundle.join(PRODUCT_VERSION_RESOURCE))
        .map_err(|e| UpdateError::new(failure, e))?;
    let mut version = String::new();
    file.take(PRODUCT_VERSION_LIMIT + 1)
        .read_to_string(&mut version)
        .map_err(|e| UpdateError::new(failure, e))?;
    if version.len() as u64 > PRODUCT_VERSION_LIMIT {
        return Err(UpdateError::new(
            failure,
            "The bundle's product version resource is too large",
        ));
    }
    Ok(version.trim().to_string())
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
        ".paintedwolf-update-{:x}-uid-{}-",
        Sha256::digest(target.as_os_str().as_encoded_bytes()),
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
pub fn current_user_directory(path: &Path) -> bool {
    fs::symlink_metadata(path).is_ok_and(|metadata| {
        #[cfg(unix)]
        {
            use std::os::unix::fs::MetadataExt;
            metadata.is_dir() && metadata.uid() == current_uid()
        }
        #[cfg(not(unix))]
        {
            metadata.is_dir()
        }
    })
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
        if super::persistence::hex_digest(id)
            && !keep.iter().any(|kept| kept == id)
            && current_user_directory(&entry.path())
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
    #[cfg(target_os = "macos")]
    {
        macos::prepare(target, candidate, &mut archive)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = target;
        Err(Failure::UnsupportedInstallation.into())
    }
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
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        if !output.status.success() {
            return Err(UpdateError::new(
                Failure::VerificationFailed,
                codesign_summary(&output.stderr),
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
/// The verdict line of a codesign failure, without the paths it was asked about.
#[cfg(target_os = "macos")]
fn codesign_summary(stderr: &[u8]) -> String {
    let text = String::from_utf8_lossy(stderr);
    text.lines()
        .filter_map(|line| line.rsplit_once(": ").map(|(_, verdict)| verdict.trim()))
        .find(|verdict| !verdict.is_empty())
        .unwrap_or("code signature verification failed")
        .to_string()
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
                .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
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
/// Production bundles must carry a Developer ID Application signature from the running team.
#[cfg(target_os = "macos")]
fn publisher_requirement(team: Option<&str>, development: bool) -> Result<String, UpdateError> {
    let identifier = "identifier \"dev.paintedwolf.code\"";
    match team {
        Some(team)
            if team.len() == 10
                && team
                    .bytes()
                    .all(|b| b.is_ascii_uppercase() || b.is_ascii_digit()) =>
        {
            Ok(format!(
                "{identifier} and anchor apple generic \
                 and certificate 1[field.1.2.840.113635.100.6.2.6] \
                 and certificate leaf[field.1.2.840.113635.100.6.1.13] \
                 and certificate leaf[subject.OU] = \"{team}\""
            ))
        }
        Some("not set") | None if development => Ok(identifier.into()),
        _ => Err(Failure::VerificationFailed.into()),
    }
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
    #[test]
    fn product_version_reads_are_bounded() {
        let root = crate::test_support::TempDir::new("update-product-version");
        let resource = root.join(PRODUCT_VERSION_RESOURCE);
        fs::create_dir_all(resource.parent().unwrap()).unwrap();
        fs::write(&resource, "1.2.3\n").unwrap();
        assert_eq!(
            product_version(&root, Failure::StateUnavailable).unwrap(),
            "1.2.3"
        );
        fs::write(&resource, "9".repeat(PRODUCT_VERSION_LIMIT as usize + 1)).unwrap();
        assert_eq!(
            product_version(&root, Failure::VerificationFailed)
                .unwrap_err()
                .code,
            Failure::VerificationFailed
        );
    }
    #[cfg(target_os = "macos")]
    #[test]
    fn production_requires_a_developer_id_publisher_and_cannot_inject_a_requirement() {
        let requirement = publisher_requirement(Some("TEAM123456"), false).unwrap();
        assert!(requirement.contains("certificate leaf[subject.OU] = \"TEAM123456\""));
        assert!(requirement.contains("certificate 1[field.1.2.840.113635.100.6.2.6]"));
        assert!(publisher_requirement(Some("not set"), false).is_err());
        assert!(publisher_requirement(None, false).is_err());
        assert!(publisher_requirement(Some("TEAM\" or true"), false).is_err());
        assert!(publisher_requirement(Some("not set"), true).is_ok());
    }
    #[cfg(target_os = "macos")]
    #[test]
    fn codesign_failures_are_summarized_without_paths() {
        let stderr = b"/Applications/Painted Wolf Code.app: invalid signature (code or signature have been modified)\nIn architecture: arm64\n";
        assert_eq!(
            codesign_summary(stderr),
            "invalid signature (code or signature have been modified)"
        );
        assert_eq!(codesign_summary(b""), "code signature verification failed");
    }
}
