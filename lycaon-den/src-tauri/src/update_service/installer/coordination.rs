//! Installation leases bind directory inodes across accounts and survive directory exchange.
use super::{Failure, UpdateError};
use std::{
    fs,
    os::unix::fs::{MetadataExt, OpenOptionsExt},
    path::Path,
};

pub struct InstallationLease(pub fs::File);
fn directory(path: &Path) -> Result<fs::File, UpdateError> {
    fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))
}
fn lock(
    file: fs::File,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    if nonblocking {
        let result = if exclusive {
            file.try_lock()
        } else {
            file.try_lock_shared()
        };
        result.map_err(|error| match error {
            fs::TryLockError::WouldBlock => Failure::InvalidTransition.into(),
            fs::TryLockError::Error(error) => UpdateError::new(Failure::StateUnavailable, error),
        })?;
    } else {
        (if exclusive {
            file.lock()
        } else {
            file.lock_shared()
        })
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    }
    Ok(InstallationLease(file))
}
pub fn acquire_gate(target: &Path) -> Result<InstallationLease, UpdateError> {
    lock(
        directory(target.parent().ok_or(Failure::UnsupportedInstallation)?)?,
        true,
        false,
    )
}
pub fn acquire_lease(
    target: &Path,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    lock(directory(target)?, exclusive, nonblocking)
}
pub fn acquire_preparation(_: &Path) -> Result<InstallationLease, UpdateError> {
    let dir = super::super::persistence::update_dir()?;
    lock(
        super::super::record_lock::open(&dir, "preparation.lock")?,
        true,
        true,
    )
}
pub fn try_activation(target: &Path) -> Result<Option<InstallationLease>, UpdateError> {
    let _gate = acquire_gate(target)?;
    match acquire_lease(target, true, true) {
        Ok(lease) => Ok(Some(lease)),
        Err(error) if error.code == Failure::InvalidTransition => Ok(None),
        Err(error) => Err(error),
    }
}
pub fn downgrade(lease: &InstallationLease) -> Result<(), UpdateError> {
    lease
        .0
        .lock_shared()
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))
}
fn matches(file: &fs::File, path: &Path) -> bool {
    file.metadata()
        .and_then(|a| {
            fs::symlink_metadata(path)
                .map(|b| b.is_dir() && a.dev() == b.dev() && a.ino() == b.ino())
        })
        .unwrap_or(false)
}
pub fn exchange_coordinated(
    lease: &mut InstallationLease,
    target: &Path,
    prepared: &Path,
) -> Result<(), UpdateError> {
    let _gate = acquire_gate(target)?;
    if !matches(&lease.0, target) {
        return Err(Failure::CandidateChanged.into());
    }
    let next = acquire_lease(prepared, true, true)?;
    let result = super::macos::exchange(target, prepared);
    // The exchange can succeed even if syncing the parent directory fails.
    if matches(&next.0, target) {
        *lease = next;
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn process_contender() {
        let Some(path) = std::env::var_os("PW_TEST_INSTALLATION") else {
            return;
        };
        let path = std::path::PathBuf::from(path);
        assert!(try_activation(&path).unwrap().is_none());
        assert!(acquire_lease(&path, false, true).is_ok());
        // A failed exclusive attempt releases the gate immediately.
        assert!(acquire_gate(&path).is_ok());
    }
    #[test]
    fn a_separate_process_observes_the_bundle_lease() {
        use std::os::unix::fs::PermissionsExt;
        let root = crate::test_support::TempDir::new("update-process-lease");
        let app = root.join("App.app");
        fs::create_dir(&app).unwrap();
        fs::set_permissions(&app, fs::Permissions::from_mode(0o555)).unwrap();
        let lease = acquire_lease(&app, false, true).unwrap();
        let status = std::process::Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "update_service::installer::coordination::tests::process_contender",
            ])
            .env("PW_TEST_INSTALLATION", &app)
            .status()
            .unwrap();
        assert!(status.success());
        drop(lease);
        assert!(try_activation(&app).unwrap().is_some());
    }
    #[test]
    fn symlinked_bundle_is_not_a_coordination_root() {
        let root = crate::test_support::TempDir::new("update-symlink-lease");
        let app = root.join("App.app");
        std::os::unix::fs::symlink(&*root, &app).unwrap();
        assert!(acquire_lease(&app, false, true).is_err());
    }
    #[test]
    fn separate_handles_coordinate_without_a_profile_directory() {
        let root = crate::test_support::TempDir::new("update-directory-lease");
        let app = root.join("App.app");
        fs::create_dir(&app).unwrap();
        let first = acquire_lease(&app, false, true).unwrap();
        let second = acquire_lease(&app, false, true).unwrap();
        assert!(try_activation(&app).unwrap().is_none());
        drop(first);
        assert!(try_activation(&app).unwrap().is_none());
        drop(second);
        let exclusive = try_activation(&app).unwrap().unwrap();
        assert!(acquire_lease(&app, false, true).is_err());
        let gate = acquire_gate(&app).unwrap();
        downgrade(&exclusive).unwrap();
        drop(gate);
        assert!(acquire_lease(&app, false, true).is_ok());
    }
    #[test]
    fn exchanged_bundle_keeps_exclusive_admission() {
        let root = crate::test_support::TempDir::new("update-exchange-admission");
        let app = root.join("App.app");
        let next = root.join("Next.app");
        fs::create_dir(&app).unwrap();
        fs::create_dir(&next).unwrap();
        let mut lease = try_activation(&app).unwrap().unwrap();
        exchange_coordinated(&mut lease, &app, &next).unwrap();
        assert!(acquire_lease(&app, false, true).is_err());
        assert!(acquire_lease(&next, false, true).is_ok());
        drop(lease);
        assert!(acquire_lease(&app, false, true).is_ok());
    }
}
