//! Bundle exchange and recovery follow the installed bytes.
use super::journal::{Phase, Transaction};
#[cfg(target_os = "macos")]
use super::{admission::Lease, journal::write};
use crate::update_service::{
    installer::{self, executable, hash},
    Candidate, Failure, UpdateError,
};
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
            if installer::current_user_installation(&prepared)
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
                    if !installer::current_user_installation(&prepared) {
                        return Err(Failure::VerificationFailed.into());
                    }
                    installer::remove_installation(&prepared)?;
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
#[cfg(target_os = "macos")]
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
    t.phase = activation_failure_phase(t);
    t.error = Some(error.to_string());
    write(t)
}
pub(super) fn installed_candidate_matches(t: &Transaction) -> Result<bool, UpdateError> {
    Ok(hash(&executable(&t.target))? == t.next_hash
        && installer::bundle_hash(&t.target)? == t.next_bundle_hash)
}
fn activation_failure_phase(t: &Transaction) -> Phase {
    if installed_candidate_matches(t).unwrap_or(false) {
        Phase::Activated
    } else {
        Phase::Failed
    }
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn failed_activation_requires_the_complete_candidate_before_recording_activation() {
        let root = crate::test_support::TempDir::new("update-failure-identity");
        let t = super::super::tests::fixture(&root);
        assert_eq!(activation_failure_phase(&t), Phase::Failed);
        installer::exchange_coordinated(
            &mut installer::try_activation(&t.target).unwrap().unwrap(),
            &t.target,
            &t.prepared_bundle,
        )
        .unwrap();
        assert!(installed_candidate_matches(&t).unwrap());
        assert_eq!(activation_failure_phase(&t), Phase::Activated);

        let previous = installer::bundle_hash(&t.prepared_bundle).unwrap();
        fs::write(t.target.join("changed-resource"), "corrupted").unwrap();
        assert_eq!(hash(&executable(&t.target)).unwrap(), t.next_hash);
        assert!(!installed_candidate_matches(&t).unwrap());
        assert_eq!(activation_failure_phase(&t), Phase::Failed);
        assert_eq!(installer::bundle_hash(&t.prepared_bundle).unwrap(), previous);
        fs::remove_dir_all(&t.target).unwrap();
        assert!(installed_candidate_matches(&t).is_err());
        assert_eq!(activation_failure_phase(&t), Phase::Failed);
    }
}
