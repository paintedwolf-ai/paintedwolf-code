//! macOS destination checks, bundle preparation, and atomic exchange.
use super::{
    archive, bundle_hash, executable, hash, prepared_path, prepared_prefix, verify_bundle,
    Candidate, Failure, PreparedIdentity, UpdateError,
};
use std::{
    ffi::CString,
    fs,
    os::unix::{ffi::OsStrExt, fs::PermissionsExt},
    path::Path,
};

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
            && super::current_user_directory(&entry.path())
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
        if !super::current_user_directory(&destination) {
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
}
