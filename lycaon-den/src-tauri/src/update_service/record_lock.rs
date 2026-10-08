//! Stable lock files serialize mutations of private operational records.
use super::{Failure, UpdateError};
use std::{
    fs::{File, OpenOptions, TryLockError},
    path::Path,
    time::Duration,
};

pub fn open(dir: &Path, name: &str) -> Result<File, UpdateError> {
    crate::config_dir::ensure_private_dir(dir)
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    let mut options = OpenOptions::new();
    options.read(true).write(true).create(true).truncate(false);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options
            .mode(0o600)
            .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC);
    }
    let file = options
        .open(dir.join(name))
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    if !file.metadata().is_ok_and(|m| m.is_file()) {
        return Err(Failure::StateUnavailable.into());
    }
    Ok(file)
}

pub fn acquire(dir: &Path, name: &str) -> Result<File, UpdateError> {
    let file = open(dir, name)?;
    file.lock()
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    Ok(file)
}

pub async fn acquire_async(dir: &Path, name: &str) -> Result<File, UpdateError> {
    let file = open(dir, name)?;
    loop {
        match file.try_lock() {
            Ok(()) => return Ok(file),
            Err(TryLockError::WouldBlock) => tokio::time::sleep(Duration::from_millis(20)).await,
            Err(TryLockError::Error(e)) => {
                return Err(UpdateError::new(Failure::StateUnavailable, e))
            }
        }
    }
}
