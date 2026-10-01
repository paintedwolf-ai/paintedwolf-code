use super::*;
use super::{check::*, persistence::*};
use crate::test_support::TempDir;
use std::fs;

#[test]
fn manifest_withdrawal_stops_discovery_and_rejects_malformed_state() {
    let offered = serde_json::json!({"update_keys": {"signing_generation": 1}});
    assert!(manifest_offers_update(&offered, 1).unwrap());
    assert!(manifest_offers_update(&offered, 2).is_err());
    let mut halted = offered.clone();
    halted["withdrawn"] = serde_json::json!(true);
    assert!(!manifest_offers_update(&halted, 1).unwrap());
    for invalid in [
        serde_json::json!(false),
        serde_json::json!("true"),
        serde_json::Value::Null,
    ] {
        halted["withdrawn"] = invalid;
        assert!(manifest_offers_update(&halted, 1).is_err());
    }
}

fn check_fixture() -> Inner<&'static str> {
    Inner {
        state: NativeUpdateState::idle(
            "1.0.0".into(),
            InstallSource::DirectDownload,
            UpdateChannel::Stable,
        ),
        candidate: None,
        check_epoch: 0,
        rollout_bucket: 99,
    }
}

#[test]
fn manual_recovery_check_works_when_automatic_checks_are_off() {
    let mut inner = check_fixture();
    inner.set_checks_enabled(false);
    assert!(inner.begin_check(true).unwrap().is_none());
    let ticket = inner.begin_check(false).unwrap().unwrap();
    assert!(inner.finish_check(
        ticket,
        release("1.1.0"),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::Available);
    assert!(!inner.state.checks_enabled);
}

#[test]
fn install_recheck_rejects_withdrawal_replacement_substitution_and_network_failure() {
    for (result, exact_identity, expected_phase, expected_offer) in [
        (Ok(None), true, UpdatePhase::UpToDate, None),
        (
            release("1.2.0"),
            false,
            UpdatePhase::Available,
            Some("1.2.0"),
        ),
        (
            release("1.1.0"),
            false,
            UpdatePhase::Available,
            Some("1.1.0"),
        ),
        (
            Err(UpdateError::new(Failure::CheckFailed, "offline")),
            true,
            UpdatePhase::Available,
            Some("1.1.0"),
        ),
    ] {
        let mut inner = check_fixture();
        surface_release(&mut inner);
        let ticket = inner.begin_check(false).unwrap().unwrap();
        assert!(!inner.finish_install_check(
            ticket,
            result,
            InstallSource::DirectDownload,
            None,
            |_| exact_identity
        ));
        assert_eq!(inner.state.phase, expected_phase);
        assert_eq!(inner.state.available_version.as_deref(), expected_offer);
    }
    let mut inner = check_fixture();
    surface_release(&mut inner);
    inner.state.checks_enabled = false;
    let ticket = inner.begin_check(false).unwrap().unwrap();
    assert!(inner.finish_install_check(
        ticket,
        release("1.1.0"),
        InstallSource::DirectDownload,
        None,
        |_| true
    ));
    assert!(!inner.state.checks_enabled);
}

#[test]
fn install_recheck_refuses_package_source_change() {
    for source in [InstallSource::HomebrewCask, InstallSource::Unknown] {
        let mut inner = check_fixture();
        surface_release(&mut inner);
        let ticket = inner.begin_check(false).unwrap().unwrap();
        assert!(!inner.finish_install_check(ticket, release("1.1.0"), source, None, |_| true));
        assert_eq!(inner.state.install_source, source);
    }
}

#[test]
fn compiled_key_generation_selects_independent_static_feeds() {
    let (number, _) = embedded_key();
    assert!(number > 0);
    for (channel, label) in [
        (UpdateChannel::Stable, "stable"),
        (UpdateChannel::Preview, "preview"),
    ] {
        assert_eq!(
            channel.endpoint(),
            format!("{DOWNLOAD_ORIGIN}/updates/{label}/key-{number}/latest.json")
        );
    }
}

#[test]
fn release_maturity_selects_only_the_initial_channel() {
    assert_eq!(default_channel("1.0.0"), Ok(UpdateChannel::Stable));
    assert_eq!(default_channel("1.0.0-rc.1"), Ok(UpdateChannel::Preview));
    assert!(default_channel("1.0").is_err());
}

#[test]
fn changing_channel_invalidates_a_retained_candidate() {
    let mut inner = check_fixture();
    surface_release(&mut inner);
    inner.set_channel(UpdateChannel::Preview);
    assert_eq!(inner.state.channel, UpdateChannel::Preview);
    assert_eq!(inner.state.phase, UpdatePhase::Idle);
    assert_eq!(inner.state.available_version, None);
    assert_eq!(inner.candidate, None);
}

fn release(version: &'static str) -> Result<Option<CheckedCandidate<&'static str>>, UpdateError> {
    Ok(Some(CheckedCandidate {
        candidate: version,
        current_version: "1.0.0".into(),
        version: version.into(),
        notes: Some(format!("Notes for {version}")),
        age_secs: Some(0),
    }))
}

fn surface_release(inner: &mut Inner<&'static str>) {
    let ticket = inner
        .begin_check(false)
        .expect("begin manual check")
        .expect("check ticket");
    assert!(inner.finish_check(
        ticket,
        release("1.1.0"),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::Available);
}

#[test]
fn automatic_check_keeps_a_manually_surfaced_release() {
    let mut inner = check_fixture();
    surface_release(&mut inner);
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("check ticket");
    assert_eq!(inner.candidate, Some("1.1.0"));
    assert_eq!(inner.state.phase, UpdatePhase::Checking);
    assert!(inner.finish_check(
        ticket,
        release("1.1.0"),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::Available);
    assert_eq!(
        inner.state.rollout_eligibility,
        RolloutEligibility::Eligible
    );
    assert_eq!(inner.state.available_version.as_deref(), Some("1.1.0"));
    assert_eq!(inner.candidate, Some("1.1.0"));
}

#[test]
fn held_newer_release_does_not_withdraw_the_offered_candidate() {
    let mut inner = check_fixture();
    surface_release(&mut inner);
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("check ticket");
    assert!(inner.finish_check(
        ticket,
        release("1.2.0"),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::Available);
    assert_eq!(inner.state.available_version.as_deref(), Some("1.1.0"));
    assert_eq!(inner.candidate, Some("1.1.0"));
    let ticket = inner
        .begin_check(false)
        .expect("begin manual check")
        .expect("check ticket");
    assert!(inner.finish_check(
        ticket,
        release("1.2.0"),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.available_version.as_deref(), Some("1.2.0"));
    assert_eq!(inner.candidate, Some("1.2.0"));
}

#[test]
fn failed_recheck_retains_installable_candidate_and_retries_soon() {
    let mut inner = check_fixture();
    surface_release(&mut inner);
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("check ticket");
    assert!(inner.finish_check(
        ticket,
        Err(UpdateError::new(Failure::CheckFailed, "offline")),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::Available);
    assert_eq!(
        inner.state.error.as_ref().map(|error| error.code),
        Some(Failure::CheckFailed)
    );
    assert_eq!(inner.state.notes.as_deref(), Some("Notes for 1.1.0"));
    assert_eq!(inner.candidate, Some("1.1.0"));
    assert!(install_transition_allowed(inner.state.phase));
    assert_eq!(next_automatic_delay(&inner.state), RETRY_INTERVAL);
}

#[test]
fn authoritative_no_update_clears_an_offered_candidate() {
    let mut inner = check_fixture();
    surface_release(&mut inner);
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("check ticket");
    assert!(inner.finish_check(ticket, Ok(None), InstallSource::DirectDownload, None));
    assert_eq!(inner.state.phase, UpdatePhase::UpToDate);
    assert_eq!(inner.state.available_version, None);
    assert_eq!(inner.candidate, None);
}

#[test]
fn stale_result_cannot_clear_new_check_or_revive_disabled_candidate() {
    for result in [
        release("1.1.0"),
        Err(UpdateError::new(Failure::CheckFailed, "offline")),
        Ok(None),
    ] {
        let mut inner = check_fixture();
        let old = inner
            .begin_check(false)
            .expect("begin old check")
            .expect("old ticket");
        inner.set_checks_enabled(false);
        inner.set_checks_enabled(true);
        let current = inner
            .begin_check(false)
            .expect("begin current check")
            .expect("current ticket");
        let checking = inner.state.clone();
        assert!(!inner.finish_check(old, result, InstallSource::DirectDownload, None));
        assert_eq!(inner.state, checking);
        assert!(inner.finish_check(
            current,
            release("1.2.0"),
            InstallSource::DirectDownload,
            None
        ));
        assert_eq!(inner.candidate, Some("1.2.0"));
        let disabled = inner
            .begin_check(true)
            .expect("begin check before opt out")
            .expect("ticket");
        inner.set_checks_enabled(false);
        assert!(!inner.finish_check(
            disabled,
            release("1.2.0"),
            InstallSource::DirectDownload,
            None
        ));
        assert_eq!(inner.state.phase, UpdatePhase::Idle);
        assert_eq!(inner.candidate, None);
        assert!(!inner.state.checks_enabled);
    }
}

#[test]
fn unavailable_and_held_checks_without_an_offered_candidate_stay_honest() {
    let mut inner = check_fixture();
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("ticket");
    assert!(inner
        .begin_check(true)
        .expect("skip duplicate auto check")
        .is_none());
    assert!(inner.begin_check(false).is_err());
    assert!(inner.finish_check(
        ticket,
        release("1.1.0"),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::HeldBack);
    assert_eq!(inner.candidate, None);
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("ticket");
    assert!(inner.finish_check(
        ticket,
        Err(UpdateError::new(Failure::CheckFailed, "offline")),
        InstallSource::DirectDownload,
        None
    ));
    assert_eq!(inner.state.phase, UpdatePhase::Unavailable);
    assert_eq!(inner.candidate, None);
    assert_eq!(inner.state.available_version, None);
}

#[test]
fn reading_state_during_check_or_install_keeps_the_candidate() {
    let dir = TempDir::new("update-service-read-pending-state");
    let mut inner = check_fixture();
    surface_release(&mut inner);
    let ticket = inner
        .begin_check(true)
        .expect("begin auto check")
        .expect("ticket");
    inner.reconcile_state(&dir, "1.0.0");
    assert_eq!(inner.candidate, Some("1.1.0"));
    assert!(inner.finish_check(
        ticket,
        release("1.1.0"),
        InstallSource::DirectDownload,
        None
    ));
    inner.state.phase = UpdatePhase::Installing;
    write_journal(
        &dir,
        &UpdateJournal {
            to_version: "1.1.0".into(),
            phase: UpdateJournalPhase::Installing,
        },
    )
    .expect("write active journal");
    inner.reconcile_state(&dir, "1.0.0");
    assert_eq!(inner.state.phase, UpdatePhase::Installing);
    assert_eq!(inner.candidate, Some("1.1.0"));
    assert!(dir.join(JOURNAL_FILE).exists());
}

#[test]
fn verified_installation_remains_restart_only_if_final_journal_write_failed() {
    let dir = TempDir::new("update-restart-journal-failure");
    write_journal(
        &dir,
        &UpdateJournal {
            to_version: "1.1.0".into(),
            phase: UpdateJournalPhase::Installing,
        },
    )
    .unwrap();
    let mut inner = check_fixture();
    inner.state.phase = UpdatePhase::RestartRequired;
    inner.state.available_version = Some("1.1.0".into());
    inner.state.error = Some(Failure::JournalUnavailable.into());
    inner.reconcile_state(&dir, "1.0.0");
    assert_eq!(inner.state.phase, UpdatePhase::RestartRequired);
    assert!(inner.state.error.is_some());
    inner.set_channel(UpdateChannel::Preview);
    assert_eq!(inner.state.phase, UpdatePhase::RestartRequired);
    assert_eq!(inner.state.available_version.as_deref(), Some("1.1.0"));
    assert!(inner.begin_check(false).is_err());
    assert!(inner.begin_check(true).unwrap().is_none());
}

#[test]
fn install_source_requires_an_explicit_receipt() {
    let dir = TempDir::new("update-service-install-source");
    assert_eq!(
        read_install_source(&dir),
        Ok((InstallSource::DirectDownload, None))
    );
    fs::write(
        dir.join(INSTALL_SOURCE_FILE),
        br#"{"install_source":"homebrew_cask","release_channel":"preview"}"#,
    )
    .expect("write install-source receipt");
    assert_eq!(
        read_install_source(&dir),
        Ok((InstallSource::HomebrewCask, Some(UpdateChannel::Preview)))
    );
}

#[test]
fn malformed_install_source_receipt_fails_closed() {
    let dir = TempDir::new("update-service-install-source-malformed");
    fs::write(
        dir.join(INSTALL_SOURCE_FILE),
        br#"{"install_source":"other"}"#,
    )
    .expect("write invalid install-source receipt");
    assert_eq!(
        read_install_source(&dir)
            .expect_err("reject invalid receipt")
            .code,
        Failure::InstallSourceUnavailable
    );
}

#[test]
fn transitions_hold_restart_required_until_relaunch() {
    assert!(!check_transition_allowed(UpdatePhase::Checking));
    assert!(!check_transition_allowed(UpdatePhase::RestartRequired));
    assert!(!install_transition_allowed(UpdatePhase::RestartRequired));
    assert!(check_transition_allowed(UpdatePhase::Available));
    assert!(install_transition_allowed(UpdatePhase::Available));
}

#[test]
fn automatic_check_cadence_is_bounded_and_respects_opt_out() {
    let mut state = NativeUpdateState::idle(
        "1.0.0".into(),
        InstallSource::DirectDownload,
        UpdateChannel::Stable,
    );
    assert_eq!(next_automatic_delay(&state), CHECK_INTERVAL);
    state.phase = UpdatePhase::Unavailable;
    assert_eq!(next_automatic_delay(&state), RETRY_INTERVAL);
    state.checks_enabled = false;
    assert_eq!(next_automatic_delay(&state), DISABLED_POLL_INTERVAL);
}

#[test]
fn preferences_round_trip() {
    let dir = TempDir::new("update-service-preferences");
    write_preferences(
        &dir,
        &UpdatePreferences {
            checks_enabled: false,
            channel: UpdateChannel::Preview,
            rollout_bucket: 42,
        },
    )
    .expect("write preferences");
    let preferences = load_preferences(&dir, UpdateChannel::Stable, 7).expect("load preferences");
    assert!(!preferences.checks_enabled);
    assert_eq!(preferences.channel, UpdateChannel::Preview);
    assert_eq!(preferences.rollout_bucket, 42);
}

#[test]
fn preferences_require_a_rollout_bucket() {
    let raw = br#"{"checks_enabled":true,"channel":"stable"}"#;
    assert!(serde_json::from_slice::<UpdatePreferences>(raw).is_err());
}

#[test]
fn missing_preferences_are_created() {
    let dir = TempDir::new("update-service-preferences-create");
    let preferences =
        load_preferences(&dir, UpdateChannel::Preview, 37).expect("create preferences");
    assert_eq!(preferences.channel, UpdateChannel::Preview);
    assert_eq!(preferences.rollout_bucket, 37);
    assert!(dir.join(PREFERENCES_FILE).is_file());
}

#[test]
fn rollout_ramp_widens_with_manifest_age() {
    const DAY: u64 = 24 * 60 * 60;
    assert_eq!(rollout_percent(0), 10);
    assert_eq!(rollout_percent(DAY - 1), 10);
    assert_eq!(rollout_percent(DAY), 50);
    assert_eq!(rollout_percent(2 * DAY - 1), 50);
    assert_eq!(rollout_percent(2 * DAY), 100);

    assert!(rollout_admits(9, Some(0)));
    assert!(!rollout_admits(10, Some(0)));
    assert!(rollout_admits(49, Some(DAY)));
    assert!(!rollout_admits(50, Some(DAY)));
    assert!(rollout_admits(99, Some(2 * DAY)));
    assert!(rollout_admits(99, None));
}

#[test]
fn rollout_bucket_draw_stays_in_range() {
    for _ in 0..1000 {
        assert!(draw_rollout_bucket() < ROLLOUT_BUCKETS);
    }
}

#[test]
fn manifest_age_clamps_future_dates_to_zero() {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("clock after epoch")
        .as_secs() as i64;
    assert_eq!(manifest_age_secs(Some(now + 3600)), Some(0));
    assert!(manifest_age_secs(Some(now - 3600)).expect("past date") >= 3600);
    assert_eq!(manifest_age_secs(None), None);
}

#[test]
fn persisted_update_documents_reject_unknown_fields() {
    let journal = br#"{"to_version":"0.9.0","phase":"installing","extra":true}"#;
    assert!(serde_json::from_slice::<UpdateJournal>(journal).is_err());
    let preferences = br#"{"checks_enabled":true,"extra":true}"#;
    assert!(serde_json::from_slice::<UpdatePreferences>(preferences).is_err());
}

#[test]
fn restart_journal_waits_for_target_version() {
    let dir = TempDir::new("update-service-journal");
    write_journal(
        &dir,
        &UpdateJournal {
            to_version: "0.9.0".into(),
            phase: UpdateJournalPhase::RestartRequired,
        },
    )
    .expect("write journal");

    let mut state = NativeUpdateState::idle(
        "0.8.0".into(),
        InstallSource::DirectDownload,
        UpdateChannel::Stable,
    );
    reconcile_journal(&dir, "0.8.0", &mut state).expect("reconcile current version");
    assert_eq!(state.phase, UpdatePhase::RestartRequired);

    state.checks_enabled = false;
    reconcile_journal(&dir, "0.9.0", &mut state).expect("reconcile target binary");
    assert_eq!(state.phase, UpdatePhase::Idle);
    assert!(!state.checks_enabled);
    assert!(!dir.join(JOURNAL_FILE).exists());
}

#[test]
fn restart_journal_accepts_a_newer_running_version() {
    let dir = TempDir::new("update-service-newer-version");
    write_journal(
        &dir,
        &UpdateJournal {
            to_version: "0.9.0".into(),
            phase: UpdateJournalPhase::RestartRequired,
        },
    )
    .expect("write journal");
    let mut state = NativeUpdateState::idle(
        "1.0.0".into(),
        InstallSource::DirectDownload,
        UpdateChannel::Stable,
    );
    reconcile_journal(&dir, "1.0.0", &mut state).expect("reconcile newer version");
    assert_eq!(state.phase, UpdatePhase::Idle);
    assert!(!dir.join(JOURNAL_FILE).exists());
}

#[test]
fn malformed_journal_version_fails_closed() {
    let dir = TempDir::new("update-service-malformed-journal-version");
    fs::write(
        dir.join(JOURNAL_FILE),
        br#"{"to_version":"current","phase":"restart_required"}"#,
    )
    .expect("write malformed journal");
    let mut state = NativeUpdateState::idle(
        "0.8.0".into(),
        InstallSource::DirectDownload,
        UpdateChannel::Stable,
    );

    let err =
        reconcile_journal(&dir, "0.8.0", &mut state).expect_err("reject malformed journal version");

    assert_eq!(err.code, Failure::JournalUnavailable);
    assert_eq!(state.phase, UpdatePhase::Idle);
    assert!(dir.join(JOURNAL_FILE).exists());
}

#[test]
fn interrupted_install_returns_to_idle_without_claiming_success() {
    let dir = TempDir::new("update-service-interrupted");
    write_journal(
        &dir,
        &UpdateJournal {
            to_version: "0.9.0".into(),
            phase: UpdateJournalPhase::Installing,
        },
    )
    .expect("write journal");
    let mut state = NativeUpdateState::idle(
        "0.8.0".into(),
        InstallSource::DirectDownload,
        UpdateChannel::Stable,
    );
    reconcile_journal(&dir, "0.8.0", &mut state).expect("reconcile interrupted install");
    assert_eq!(state.phase, UpdatePhase::Idle);
    assert_eq!(
        state.error.as_ref().map(|error| error.code),
        Some(Failure::Interrupted)
    );
    assert!(!dir.join(JOURNAL_FILE).exists());
}
