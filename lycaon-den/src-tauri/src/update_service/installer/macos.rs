//! macOS installation: leases on the installed bundle, preparation beside it, and the exchange.
use super::{
    archive, bundle_hash, executable, hash, prepared_path, prepared_prefix, verify_bundle,
    Candidate, Failure, PreparedIdentity, UpdateError,
};
use sha2::{Digest, Sha256};
use std::{
    ffi::CString,
    fs,
    os::{
        fd::AsRawFd,
        unix::{ffi::OsStrExt, fs::PermissionsExt},
    },
    path::{Path, PathBuf},
};
pub struct InstallationLease(pub fs::File);

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

/// One process prepares an installation at a time.
pub fn acquire_preparation(target: &Path) -> Result<InstallationLease, UpdateError> {
    acquire_named_lease(target, "preparation", true, true)
}
/// The gate serializes launches and helper admission so a lease downgrade cannot be raced.
pub fn acquire_gate(target: &Path) -> Result<InstallationLease, UpdateError> {
    acquire_named_lease(target, "gate", true, false)
}
/// Every running process holds the lifetime lease shared; activation needs it exclusively.
pub fn acquire_lease(
    target: &Path,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    acquire_named_lease(target, "lifetime", exclusive, nonblocking)
}
/// Takes the gate and, if no process is alive, the exclusive lifetime lease.
pub fn try_activation(
    target: &Path,
) -> Result<Option<(InstallationLease, InstallationLease)>, UpdateError> {
    try_activation_at(&lock_path(target, "gate")?, &lock_path(target, "lifetime")?)
}
fn try_activation_at(
    gate: &Path,
    lifetime: &Path,
) -> Result<Option<(InstallationLease, InstallationLease)>, UpdateError> {
    let gate = acquire_lock(gate, true, false)?;
    match acquire_lock(lifetime, true, true) {
        Ok(lease) => Ok(Some((gate, lease))),
        Err(error) if error.code == Failure::InvalidTransition => Ok(None),
        Err(error) => Err(error),
    }
}
fn lock_path(target: &Path, kind: &str) -> Result<PathBuf, UpdateError> {
    let root = super::super::persistence::update_dir()?;
    if !root.exists() {
        crate::config_dir::ensure_private_dir(&root)
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    }
    let id = hex::encode(Sha256::digest(target.as_os_str().as_bytes()));
    Ok(root.join(format!("installation-{id}-{kind}.lock")))
}
fn acquire_named_lease(
    target: &Path,
    kind: &str,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    acquire_lock(&lock_path(target, kind)?, exclusive, nonblocking)
}

fn acquire_lock(
    path: &Path,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    let file = match fs::File::open(path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .open(path)
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?,
        Err(error) => return Err(UpdateError::new(Failure::StateUnavailable, error)),
    };
    let mode = if exclusive {
        libc::LOCK_EX
    } else {
        libc::LOCK_SH
    } | if nonblocking { libc::LOCK_NB } else { 0 };
    if unsafe { libc::flock(file.as_raw_fd(), mode) } != 0 {
        let error = std::io::Error::last_os_error();
        let code = if error.kind() == std::io::ErrorKind::WouldBlock {
            Failure::InvalidTransition
        } else {
            Failure::StateUnavailable
        };
        return Err(UpdateError::new(code, error));
    }
    Ok(InstallationLease(file))
}
/// Downgrades an exclusive lifetime lease to shared; the caller holds the gate meanwhile.
pub fn downgrade(lease: &InstallationLease) -> Result<(), UpdateError> {
    if unsafe { libc::flock(lease.0.as_raw_fd(), libc::LOCK_SH) } != 0 {
        return Err(UpdateError::new(
            Failure::StateUnavailable,
            std::io::Error::last_os_error(),
        ));
    }
    Ok(())
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
            && entry.file_type().is_ok_and(|kind| kind.is_dir())
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
    fn existing_locks_do_not_require_write_access() {
        let root = crate::test_support::TempDir::new("update-readonly-lock");
        let path = root.join("lock");
        fs::write(&path, b"").unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o400)).unwrap();
        let shared = acquire_lock(&path, false, true).unwrap();
        assert!(acquire_lock(&path, true, true).is_err());
        drop(shared);
        let _exclusive = acquire_lock(&path, true, true).unwrap();
    }
    #[test]
    fn a_waiting_helper_releases_the_gate_for_new_launches() {
        let root = crate::test_support::TempDir::new("update-helper-admission");
        let gate = root.join("gate");
        let lifetime = root.join("lifetime");
        let existing = acquire_lock(&lifetime, false, true).unwrap();
        assert!(try_activation_at(&gate, &lifetime).unwrap().is_none());
        let launch = acquire_lock(&gate, true, true).unwrap();
        let second = acquire_lock(&lifetime, false, true).unwrap();
        drop(launch);
        drop(existing);
        assert!(try_activation_at(&gate, &lifetime).unwrap().is_none());
        drop(second);
        let activation = try_activation_at(&gate, &lifetime).unwrap().unwrap();
        assert!(acquire_lock(&gate, true, true).is_err());
        assert!(acquire_lock(&lifetime, false, true).is_err());
        drop(activation);
    }
    #[test]
    fn concurrent_instances_share_lifetime_while_gate_excludes_activation() {
        let root = crate::test_support::TempDir::new("update-locks");
        let lifetime = root.join("lifetime");
        let gate = root.join("gate");
        let first = acquire_lock(&lifetime, false, true).unwrap();
        assert!(acquire_lock(&lifetime, true, true).is_err());
        let second = acquire_lock(&lifetime, false, true).unwrap();
        drop(first);
        assert!(acquire_lock(&lifetime, true, true).is_err());
        drop(second);
        let admission = acquire_lock(&gate, true, true).unwrap();
        let exclusive = acquire_lock(&lifetime, true, true).unwrap();
        downgrade(&exclusive).unwrap();
        assert!(acquire_lock(&gate, true, true).is_err());
        drop(admission);
        let _helper = acquire_lock(&gate, true, true).unwrap();
        assert!(acquire_lock(&lifetime, true, true).is_err());
        assert!(acquire_lock(&lifetime, false, true).is_ok());
    }
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
