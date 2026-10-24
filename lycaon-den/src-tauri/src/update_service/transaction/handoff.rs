//! Commit occurs after work is saved and the engine stops.
use super::journal::{write, Phase, Transaction};
use crate::update_service::{persistence, Failure, UpdateError};

pub struct Activation {
    pub(super) transaction: Transaction,
    pub(super) _permit: tokio::sync::OwnedMutexGuard<()>,
}
impl Activation {
    pub fn commit(mut self) -> Result<(), UpdateError> {
        self.transaction.phase = Phase::Committed;
        write(&self.transaction)?;
        // The application signature binds Info.plist; a loose copy cannot execute.
        // The parent retains the installation lease until this helper is running.
        let result = helper_command(&self.transaction).spawn();
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

fn helper_command(transaction: &Transaction) -> std::process::Command {
    let mut command = std::process::Command::new(super::executable(&transaction.target));
    command
        .arg("--apply-update")
        .arg(&transaction.id)
        .arg(persistence::installation_id(&transaction.target))
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null());
    command
}

#[cfg(all(test, target_os = "macos"))]
mod tests {
    use super::*;

    #[test]
    fn helper_executes_inside_the_signed_transaction_bundle() {
        let root = crate::test_support::TempDir::new("signed-update-helper");
        let transaction = super::super::tests::fixture(&root);
        let command = helper_command(&transaction);
        assert_eq!(command.get_program(), super::super::executable(&transaction.target).as_os_str());
        let arguments: Vec<_> = command.get_args().collect();
        assert_eq!(arguments, [
            std::ffi::OsStr::new("--apply-update"),
            std::ffi::OsStr::new(&transaction.id),
            std::ffi::OsStr::new(&persistence::installation_id(&transaction.target)),
        ]);
    }
}
