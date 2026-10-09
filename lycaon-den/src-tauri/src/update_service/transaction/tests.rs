use super::*;
use super::{
    activation::activate_using,
    admission::{should_reopen_after_wait, superseded},
    handoff::record_spawn_failure,
    state::resume_state,
};
#[cfg(target_os = "macos")]
use super::admission::require_running_version;
use std::fs;
pub(super) fn fixture(root: &Path) -> Transaction {
    let candidate = super::super::tests::candidate("1.1.0");
    let target = root.join("Painted Wolf Code.app");
    let staged = installer::prepared_path(&target, &candidate).unwrap();
    for (bundle, contents) in [(&target, "old engine"), (&staged, "new engine")] {
        let exe = executable(bundle);
        fs::create_dir_all(exe.parent().unwrap()).unwrap();
        fs::write(exe, contents).unwrap();
    }
    Transaction {
        format_version: 2,
        prepared_bundle: staged.clone(),
        id: uuid::Uuid::new_v4().to_string(),
        candidate,
        previous_hash: hash(&executable(&target)).unwrap(),
        next_hash: hash(&executable(&staged)).unwrap(),
        next_bundle_hash: installer::bundle_hash(&staged).unwrap(),
        target,
        phase: Phase::Committed,
        relaunch: false,
        recovery_relaunch_attempted: false,
        error: None,
    }
}
fn fresh_state() -> super::super::NativeUpdateState {
    super::super::NativeUpdateState::new(
        "1.0.0".into(),
        super::super::UpdateChannel::Stable,
        super::super::InstallSource::DirectDownload,
    )
}
#[test]
fn a_waiting_helper_never_revives_revoked_or_completed_relaunch_intent() {
    let root = crate::test_support::TempDir::new("update-helper-relaunch");
    let mut t = fixture(&root);
    let id = t.id.clone();
    t.relaunch = true;
    assert!(should_reopen_after_wait(&t, &id));
    assert!(!should_reopen_after_wait(&t, "superseded-helper"));
    t.relaunch = false;
    assert!(!should_reopen_after_wait(&t, &id));
    t.relaunch = true;
    for phase in [
        Phase::Prepared,
        Phase::Activated,
        Phase::StartupConfirmed,
        Phase::Failed,
    ] {
        t.phase = phase;
        assert!(!should_reopen_after_wait(&t, &id));
    }
}
#[test]
fn external_reinstall_retires_unconfirmed_activation_even_when_returning_to_previous_version() {
    let root = crate::test_support::TempDir::new("update-reinstall");
    let mut t = fixture(&root);
    assert!(!superseded(&t).unwrap());
    t.phase = Phase::Activated;
    assert!(superseded(&t).unwrap());
    fs::write(executable(&t.target), "new engine").unwrap();
    assert!(!superseded(&t).unwrap());
    fs::write(executable(&t.target), "external signed replacement").unwrap();
    assert!(superseded(&t).unwrap());
}
#[test]
fn a_committed_journal_is_reused_only_for_its_own_installation_and_release() {
    let root = crate::test_support::TempDir::new("update-committed-reuse");
    let t = fixture(&root);
    committed_reuse(&t, &t.target, None).unwrap();
    committed_reuse(&t, &t.target, Some(&t.candidate.release_id)).unwrap();
    assert_eq!(
        committed_reuse(&t, &t.target, Some("other"))
            .unwrap_err()
            .code,
        Failure::CandidateChanged
    );
    assert_eq!(
        committed_reuse(&t, &root.join("Elsewhere.app"), None)
            .unwrap_err()
            .code,
        Failure::InvalidTransition
    );
    let mut prepared = t.clone();
    prepared.phase = Phase::Prepared;
    assert!(committed_reuse(&prepared, &t.target, None).is_err());
}
#[test]
fn a_recently_confirmed_offer_survives_an_unreachable_feed() {
    let now = 1_000_000;
    assert!(offer_is_fresh(Some(now - 3_600), now));
    assert!(offer_is_fresh(Some(now - OFFER_FRESHNESS.as_secs()), now));
    assert!(!offer_is_fresh(
        Some(now - OFFER_FRESHNESS.as_secs() - 1),
        now
    ));
    assert!(!offer_is_fresh(None, now));
    assert!(!offer_is_fresh(Some(now + 1), now));
}
#[test]
fn failed_spawn_with_failed_record_requires_recovery_instead_of_retry() {
    let root = crate::test_support::TempDir::new("update-spawn-record-failure");
    let mut transaction = fixture(&root);
    let error = record_spawn_failure(
        &mut transaction,
        std::io::Error::other("spawn failed"),
        |attempt| {
            assert_eq!(attempt.phase, Phase::Failed);
            Err(Failure::JournalUnavailable.into())
        },
    );
    assert_eq!(error.code, Failure::RecoveryRequired);
    let mut state = fresh_state();
    state.candidate = Some(transaction.candidate.clone());
    state.staged_release_id = Some(transaction.candidate.release_id);
    state.installation = Installation::AwaitingExit;
    resume_state(&mut state, Some(error), true);
    state.refresh_capabilities(true);
    assert_eq!(state.installation, Installation::RecoveryRequired);
    assert!(!state.capabilities.can_check);
    assert!(!state.capabilities.can_download);
    assert!(!state.capabilities.can_restart_to_update);
    assert!(!state.capabilities.can_install_automatically);
}
#[test]
fn resumed_state_follows_the_journal_and_the_failure() {
    let candidate = super::super::tests::candidate("1.1.0");
    for (failure, committed, installation, restart, download) in [
        (
            Some(Failure::ActivationFailed),
            false,
            Installation::Failed,
            false,
            true,
        ),
        (
            Some(Failure::EngineStopFailed),
            false,
            Installation::Staged,
            true,
            false,
        ),
        (
            Some(Failure::Cancelled),
            false,
            Installation::Staged,
            true,
            false,
        ),
        (
            Some(Failure::Cancelled),
            true,
            Installation::Committed,
            true,
            false,
        ),
        (None, true, Installation::Committed, true, false),
    ] {
        let mut state = fresh_state();
        state.staged_release_id = Some(candidate.release_id.clone());
        state.candidate = Some(candidate.clone());
        state.installation = Installation::AwaitingExit;
        resume_state(&mut state, failure.map(UpdateError::from), committed);
        state.refresh_capabilities(true);
        assert_eq!(state.installation, installation);
        assert_eq!(state.capabilities.can_restart_to_update, restart);
        assert_eq!(state.capabilities.can_download, download);
        assert_eq!(state.last_error.map(|error| error.code), failure);
    }
}
#[test]
fn recovery_after_exchange_never_exchanges_the_old_bundle_back() {
    let root = crate::test_support::TempDir::new("update-crash-after-swap");
    let mut t = fixture(&root);
    let before = t.clone();
    assert!(activate_using(
        &mut t,
        |_, _| Ok(()),
        swap_directories,
        |_| Err(Failure::JournalUnavailable.into())
    )
    .is_err());
    assert_eq!(t.phase, Phase::Activated);
    let mut recovered = before;
    activate_using(
        &mut recovered,
        |_, _| panic!("already activated"),
        |_, _| panic!("must never roll back"),
        |_| Ok(()),
    )
    .unwrap();
    assert_eq!(recovered.phase, Phase::Activated);
    assert_eq!(
        hash(&executable(&recovered.target)).unwrap(),
        recovered.next_hash
    );
    assert_eq!(
        hash(&executable(
            &installer::prepared_path(&recovered.target, &recovered.candidate).unwrap()
        ))
        .unwrap(),
        recovered.previous_hash
    );
}
#[test]
fn failed_exchange_leaves_current_bundle_and_committed_intent() {
    let root = crate::test_support::TempDir::new("update-failed-swap");
    let mut t = fixture(&root);
    assert!(activate_using(
        &mut t,
        |_, _| Ok(()),
        |_, _| Err(Failure::ActivationFailed.into()),
        |_| panic!("not activated")
    )
    .is_err());
    assert_eq!(t.phase, Phase::Committed);
    assert_eq!(hash(&executable(&t.target)).unwrap(), t.previous_hash);
}
#[test]
fn externally_replaced_installation_is_never_overwritten() {
    let root = crate::test_support::TempDir::new("update-external-replacement");
    let mut t = fixture(&root);
    fs::write(executable(&t.target), "different installation").unwrap();
    let error = activate_using(
        &mut t,
        |_, _| panic!("unknown installation"),
        |_, _| panic!("no exchange"),
        |_| panic!("no receipt"),
    )
    .unwrap_err();
    assert_eq!(error.code, Failure::CandidateChanged);
}
// These recovery tests do not require an atomic exchange.
fn swap_directories(left: &Path, right: &Path) -> Result<(), UpdateError> {
    let parked = left.with_extension("parked");
    fs::rename(left, &parked)
        .and_then(|_| fs::rename(right, left))
        .and_then(|_| fs::rename(&parked, right))
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))
}
#[cfg(target_os = "macos")]
#[test]
fn launch_refuses_a_binary_whose_installed_bundle_has_changed() {
    let root = crate::test_support::TempDir::new("update-launch-version");
    fs::create_dir_all(root.join("Contents/Resources")).unwrap();
    let version = root.join(installer::PRODUCT_VERSION_RESOURCE);
    fs::write(&version, env!("PAINTED_WOLF_VERSION")).unwrap();
    require_running_version(&root).unwrap();
    fs::write(&version, "99.0.0").unwrap();
    assert_eq!(
        require_running_version(&root).unwrap_err().code,
        Failure::CandidateChanged
    );
}

#[test]
fn activation_requires_the_whole_prepared_bundle_to_match_its_receipt() {
    let root = crate::test_support::TempDir::new("update-prepared-bundle-changed");
    let mut t = fixture(&root);
    let prepared = t.prepared_bundle.clone();
    let error = activate_using(
        &mut t,
        |_, _| {
            fs::write(prepared.join("unexpected-resource"), "changed after preparation").unwrap();
            Ok(())
        },
        |_, _| panic!("changed bundle must not be installed"),
        |_| panic!("changed bundle must not be recorded as activated"),
    )
    .unwrap_err();
    assert_eq!(error.code, Failure::VerificationFailed);
    assert_eq!(t.phase, Phase::Committed);
    assert_eq!(hash(&executable(&t.target)).unwrap(), t.previous_hash);
}

#[test]
fn recovery_requires_the_whole_installed_bundle_to_match_its_receipt() {
    let root = crate::test_support::TempDir::new("update-installed-bundle-changed");
    let mut t = fixture(&root);
    swap_directories(&t.target, &t.prepared_bundle).unwrap();
    fs::write(t.target.join("unexpected-resource"), "changed after activation").unwrap();
    let error = activate_using(
        &mut t,
        |_, _| panic!("matching executable must not trigger preparation"),
        |_, _| panic!("recovery must not exchange the previous bundle back"),
        |_| panic!("changed bundle must not be recorded as activated"),
    )
    .unwrap_err();
    assert_eq!(error.code, Failure::VerificationFailed);
    assert_eq!(t.phase, Phase::Committed);
    assert_eq!(hash(&executable(&t.target)).unwrap(), t.next_hash);
}
