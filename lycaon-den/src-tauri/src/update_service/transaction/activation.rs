//! Bundle exchange and recovery follow the installed bytes.
use super::journal::{Phase, Transaction};
#[cfg(target_os = "macos")]
use super::{admission::Lease, journal::write};
use crate::update_service::{
    installer::{self, executable, hash},
    Candidate, Failure, UpdateError,
};
#[cfg(target_os = "macos")]
use std::fs;
use std::path::Path;

#[cfg(target_os = "macos")]
pub(super) fn activate(t: &mut Transaction, lease: &mut Lease) -> Result<(), UpdateError> {
    let expected = t.next_hash.clone();
    let expected_bundle = t.next_bundle_hash.clone();
    let recorded_prepared = t.prepared_bundle.clone();
    activate_using(
        t,
        |target, candidate| {
            let prepared = recorded_prepared;
            // The preparation receipt binds every file, link, and permission.
            if installer::owned_directory(&prepared)
                && hash(&executable(&prepared)).is_ok_and(|actual| actual == expected)
                && installer::bundle_hash(&prepared).is_ok_and(|actual| actual == expected_bundle)
                && installer::verify_bundle(&prepared).is_ok()
            {
                return Ok(());
            }
            let rebuilt = installer::prepare_at(target, candidate)?;
            let rebuilt_path = installer::prepared_path(target, candidate)?;
            if rebuilt_path != prepared {
                if prepared.exists() {
                    if !installer::owned_directory(&prepared) {
                        return Err(Failure::VerificationFailed.into());
                    }
                    fs::remove_dir_all(&prepared)
                        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
                }
                crate::atomic_file::replace(&rebuilt_path, &prepared, true)
                    .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            }
            if rebuilt.executable_hash != expected || rebuilt.bundle_hash != expected_bundle {
                return Err(Failure::VerificationFailed.into());
            }
            Ok(())
        },
        |target, prepared| installer::exchange_coordinated(lease, target, prepared),
        write,
    )
}
#[cfg(any(target_os = "macos", all(test, unix)))]
pub(super) fn activate_using(
    t: &mut Transaction,
    prepare: impl FnOnce(&Path, &Candidate) -> Result<(), UpdateError>,
    exchange: impl FnOnce(&Path, &Path) -> Result<(), UpdateError>,
    record: impl FnOnce(&Transaction) -> Result<(), UpdateError>,
) -> Result<(), UpdateError> {
    let installed = hash(&executable(&t.target))?;
    if installed != t.next_hash {
        if installed != t.previous_hash {
            return Err(Failure::CandidateChanged.into());
        }
        prepare(&t.target, &t.candidate)?;
        let prepared = t.prepared_bundle.clone();
        if hash(&executable(&prepared))? != t.next_hash
            || installer::bundle_hash(&prepared)? != t.next_bundle_hash
        {
            return Err(Failure::VerificationFailed.into());
        }
        exchange(&t.target, &prepared)?;
    } else if installer::bundle_hash(&t.target)? != t.next_bundle_hash {
        return Err(Failure::VerificationFailed.into());
    }
    t.phase = Phase::Activated;
    record(t)
}
/// The recorded phase follows the installed bytes, even after an exchange error.
#[cfg(target_os = "macos")]
pub(super) fn record_activation_failure(
    t: &mut Transaction,
    error: &UpdateError,
) -> Result<(), UpdateError> {
    t.phase = if hash(&executable(&t.target)).is_ok_and(|installed| installed == t.next_hash) {
        Phase::Activated
    } else {
        Phase::Failed
    };
    t.error = Some(error.to_string());
    write(t)
}
#[cfg(target_os = "macos")]
pub(super) fn installed_is_known(t: &Transaction) -> bool {
    hash(&executable(&t.target)).is_ok_and(|hash| {
        hash == t.previous_hash
            || (hash == t.next_hash
                && installer::bundle_hash(&t.target)
                    .is_ok_and(|digest| digest == t.next_bundle_hash))
    })
}
