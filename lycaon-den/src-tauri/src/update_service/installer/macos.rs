//! macOS destination checks, bundle preparation, and atomic exchange.
use super::{
    archive, bundle_hash, executable, hash, prepared_path, prepared_prefix, Candidate, Failure,
    PreparedIdentity, UpdateError,
};
use std::{
    ffi::CString,
    fs,
    os::unix::{ffi::OsStrExt, fs::PermissionsExt},
    path::{Path, PathBuf},
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

pub fn supported() -> bool {
    bundle().is_ok_and(|target| writable_installation(&target))
}

pub fn writable_installation(target: &Path) -> bool {
    let Some(parent) = target.parent() else {
        return false;
    };
    let Ok(parent) = CString::new(parent.as_os_str().as_bytes()) else {
        return false;
    };
    // Translocated and disk-image applications reside on a read-only mount.
    let mut stat = std::mem::MaybeUninit::<libc::statvfs>::uninit();
    unsafe {
        libc::statvfs(parent.as_ptr(), stat.as_mut_ptr()) == 0
            && stat.assume_init().f_flag & libc::ST_RDONLY == 0
            && libc::access(parent.as_ptr(), libc::W_OK | libc::X_OK) == 0
    }
}

pub fn exchange(left: &Path, right: &Path) -> Result<(), UpdateError> {
    let l = CString::new(left.as_os_str().as_bytes())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    let r = CString::new(right.as_os_str().as_bytes())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    if unsafe { libc::renamex_np(l.as_ptr(), r.as_ptr(), libc::RENAME_SWAP) } != 0 {
        return Err(UpdateError::new(
            Failure::ActivationFailed,
            std::io::Error::last_os_error(),
        ));
    }
    fs::File::open(left.parent().ok_or(Failure::ActivationFailed)?)
        .and_then(|f| f.sync_all())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))
}
pub fn prepare(
    target: &Path,
    candidate: &Candidate,
    artifact: &mut fs::File,
) -> Result<PreparedIdentity, UpdateError> {
    let parent = target.parent().ok_or(Failure::UnsupportedInstallation)?;
    super::check_space(parent, archive::expanded_size(artifact)?)?;
    let preparing_prefix = format!("{}preparing-", prepared_prefix(target));
    for entry in fs::read_dir(parent).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))? {
        let entry = entry.map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name
            .strip_prefix(&preparing_prefix)
            .is_some_and(|id| uuid::Uuid::parse_str(id).is_ok())
            && super::current_user_installation(&entry.path())
        {
            fs::remove_dir_all(entry.path())
                .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        }
    }
    let temp = parent.join(format!("{preparing_prefix}{}", uuid::Uuid::new_v4()));
    fs::create_dir(&temp).map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    fs::set_permissions(&temp, fs::Permissions::from_mode(0o700))
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let result = archive::extract(artifact, &temp)
        .and_then(|_| verify_bundle(&temp))
        .and_then(|_| verify_platform(&temp, &candidate.platform))
        .and_then(|_| {
            let version = super::product_version(&temp, Failure::VerificationFailed)?;
            if version != candidate.version {
                return Err(UpdateError::new(
                    Failure::VerificationFailed,
                    "The prepared bundle declares a different product version",
                ));
            }
            Ok(())
        });
    if let Err(error) = result {
        let _ = fs::remove_dir_all(&temp);
        return Err(error);
    }
    fs::set_permissions(&temp, fs::Permissions::from_mode(0o755))
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let identity = PreparedIdentity {
        executable_hash: hash(&executable(&temp))?,
        bundle_hash: bundle_hash(&temp)?,
    };
    let destination = prepared_path(target, candidate)?;
    if destination.exists() {
        if !super::current_user_installation(&destination) {
            return Err(Failure::VerificationFailed.into());
        }
        fs::remove_dir_all(&destination)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    }
    crate::atomic_file::replace(&temp, &destination, true)
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    Ok(identity)
}
/// The signed executable, not the feed's label, decides whether a bundle runs here.
fn verify_platform(bundle: &Path, platform: &str) -> Result<(), UpdateError> {
    let cpu = match platform {
        "darwin-aarch64" if std::env::consts::ARCH == "aarch64" => 0x0100000c,
        "darwin-x86_64" if std::env::consts::ARCH == "x86_64" => 0x01000007,
        _ => return Err(Failure::InvalidRelease.into()),
    };
    let mut file = fs::File::open(executable(bundle))
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    if !archive::contains_architecture(&mut file, cpu)
        .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?
    {
        return Err(Failure::VerificationFailed.into());
    }
    Ok(())
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn activation_exchange_keeps_both_complete_trees() {
        let dir = crate::test_support::TempDir::new("update-exchange");
        let a = dir.join("old");
        let b = dir.join("new");
        fs::create_dir(&a).unwrap();
        fs::create_dir(&b).unwrap();
        fs::write(a.join("identity"), "old").unwrap();
        fs::write(b.join("identity"), "new").unwrap();
        exchange(&a, &b).unwrap();
        assert_eq!(fs::read_to_string(a.join("identity")).unwrap(), "new");
        assert_eq!(fs::read_to_string(b.join("identity")).unwrap(), "old");
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

pub fn verify_bundle(path: &Path) -> Result<(), UpdateError> {
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
/// The verdict line of a codesign failure, without the paths it was asked about.
fn codesign_summary(stderr: &[u8]) -> String {
    let text = String::from_utf8_lossy(stderr);
    text.lines()
        .filter_map(|line| line.rsplit_once(": ").map(|(_, verdict)| verdict.trim()))
        .find(|verdict| !verdict.is_empty())
        .unwrap_or("code signature verification failed")
        .to_string()
}

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
