use super::check::{
    draw_rollout_bucket, manifest_age_secs, manifest_offers_update, rollout_admits,
    rollout_percent, ROLLOUT_BUCKETS,
};
use super::*;

fn state() -> NativeUpdateState {
    NativeUpdateState::new(
        "1.0.1".into(),
        UpdateChannel::Stable,
        InstallSource::DirectDownload,
    )
}
pub(super) fn candidate(version: &str) -> Candidate {
    let mut c = Candidate {
        release_id: String::new(),
        version: version.into(),
        channel: UpdateChannel::Stable,
        platform: "darwin-aarch64".into(),
        signing_generation: 1,
        artifact_url: "https://downloads.paintedwolf.dev/release.tar.gz".into(),
        artifact_signature: "signature".into(),
        notes: None,
        rollout_eligibility: RolloutEligibility::Eligible,
    };
    c.release_id = c.identity();
    c
}
fn offered(s: &mut NativeUpdateState, version: &str, automatic: bool, bucket: u8) {
    check::apply_offer(
        s,
        Ok(Some((candidate(version), Some(0)))),
        automatic,
        bucket,
    );
}
#[test]
fn staged_release_survives_network_failure_but_not_withdrawal() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 99);
    s.staged_release_id = s.candidate.as_ref().map(|c| c.release_id.clone());
    s.installation = Installation::Staged;
    check::apply_offer(&mut s, Err(Failure::CheckFailed.into()), true, 99);
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_restart_to_update);
    check::apply_offer(&mut s, Ok(None), true, 99);
    s.refresh_capabilities(true);
    assert!(!s.capabilities.can_restart_to_update);
    assert_eq!(s.installation, Installation::None);
}
#[test]
fn manual_offer_survives_automatic_rollout_and_newer_held_release() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 99);
    offered(&mut s, "1.1.0", true, 99);
    assert_eq!(s.discovery, Discovery::Available);
    offered(&mut s, "1.2.0", true, 99);
    assert_eq!(s.candidate.unwrap().version, "1.1.0");
}
#[test]
fn different_offer_cannot_inherit_prepared_bytes() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 0);
    s.staged_release_id = s.candidate.as_ref().map(|c| c.release_id.clone());
    s.installation = Installation::Staged;
    offered(&mut s, "1.2.0", false, 0);
    assert_eq!(s.installation, Installation::None);
    assert!(s.staged_release_id.is_none());
}
#[test]
fn disabling_automatic_updates_keeps_explicit_restart_only() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 0);
    s.staged_release_id = s.candidate.as_ref().map(|c| c.release_id.clone());
    s.installation = Installation::Staged;
    s.automatic_updates_enabled = false;
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_restart_to_update);
    assert!(!s.capabilities.can_install_automatically);
    s.install_source = InstallSource::HomebrewCask;
    s.refresh_capabilities(true);
    assert!(!s.capabilities.can_restart_to_update);
    assert!(!s.capabilities.can_download);
}
#[test]
fn release_identity_binds_every_installation_field_but_not_notes() {
    let c = candidate("1.1.0");
    let mut changed = c.clone();
    changed.notes = Some("new prose".into());
    assert_eq!(changed.identity(), c.identity());
    changed.artifact_signature.push('x');
    assert_ne!(changed.identity(), c.identity());
    changed = c.clone();
    changed.channel = UpdateChannel::Preview;
    assert_ne!(changed.identity(), c.identity());
    changed = c.clone();
    changed.platform = "windows-x86_64".into();
    assert_ne!(changed.identity(), c.identity());
    changed = c.clone();
    changed.signing_generation += 1;
    assert_ne!(changed.identity(), c.identity());
    changed = c.clone();
    changed.artifact_url.push('x');
    assert_ne!(changed.identity(), c.identity());
}
#[test]
fn maturity_selects_initial_channel_without_overriding_preferences() {
    assert_eq!(default_channel("1.0.1").unwrap(), UpdateChannel::Stable);
    assert_eq!(
        default_channel("1.1.0-beta.1").unwrap(),
        UpdateChannel::Preview
    );
}
#[test]
fn automatic_retry_is_bounded() {
    assert_eq!(scheduler::retry_delay(1), 1800);
    assert_eq!(scheduler::retry_delay(2), 3600);
    assert_eq!(scheduler::retry_delay(3), 7200);
    assert_eq!(scheduler::retry_delay(100), 21600);
}
#[test]
fn source_receipts_fail_closed() {
    let dir = crate::test_support::TempDir::new("update-source");
    assert_eq!(
        persistence::read_install_source(&dir).unwrap().0,
        InstallSource::DirectDownload
    );
    std::fs::write(
        dir.join("install-source.json"),
        r#"{"install_source":"homebrew_cask","release_channel":"preview"}"#,
    )
    .unwrap();
    assert_eq!(
        persistence::read_install_source(&dir).unwrap().0,
        InstallSource::HomebrewCask
    );
    std::fs::write(dir.join("install-source.json"), "{}").unwrap();
    assert!(persistence::read_install_source(&dir).is_err());
}
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
