//! Commit occurs after work is saved and the engine stops.
use super::journal::{write, Phase, Transaction};
use crate::update_service::{persistence, staging, Candidate, Failure, UpdateError};
use std::{fs, path::PathBuf};

pub struct Activation {
    pub(super) transaction: Transaction,
    pub(super) helper: PathBuf,
    pub(super) _permit: tokio::sync::OwnedMutexGuard<()>,
}
impl Activation {
    pub fn commit(mut self) -> Result<(), UpdateError> {
        self.transaction.phase = Phase::Committed;
        write(&self.transaction)?;
        let result = std::process::Command::new(&self.helper)
            .arg("--apply-update")
            .arg(&self.transaction.id)
            .arg(persistence::installation_id(&self.transaction.target))
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn();
        if let Err(error) = result {
            return Err(record_spawn_failure(&mut self.transaction, error, write));
        }
        Ok(())
    }
}
pub(super) fn record_spawn_failure(
    t: &mut Transaction,
    error: std::io::Error,
    record: impl FnOnce(&Transaction) -> Result<(), UpdateError>,
) -> UpdateError {
    t.phase = Phase::Failed;
    t.error = Some(error.to_string());
    match record(t) {
        Ok(()) => UpdateError::new(Failure::ActivationFailed, error),
        Err(record) => UpdateError::new(Failure::RecoveryRequired, error).with_context(record),
    }
}
/// A private copy of the running executable that applies the update after the parent exits.
pub(super) fn stage_helper(candidate: &Candidate) -> Result<PathBuf, UpdateError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let dir = staging::root(candidate)?;
        crate::config_dir::ensure_private_dir(&dir)
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let helper = dir.join("update-helper");
        match fs::symlink_metadata(&helper) {
            Ok(_) => {
                fs::remove_file(&helper).map_err(|e| UpdateError::new(Failure::InstallFailed, e))?
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(UpdateError::new(Failure::InstallFailed, e)),
        }
        let mut source = fs::File::open(
            std::env::current_exe().map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?,
        )
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let mut copy = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o700)
            .open(&helper)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        std::io::copy(&mut source, &mut copy)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        copy.sync_all()
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        Ok(helper)
    }
    #[cfg(not(unix))]
    {
        let _ = candidate;
        Err(Failure::UnsupportedInstallation.into())
    }
}
