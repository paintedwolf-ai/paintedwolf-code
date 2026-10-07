use super::check::{draw_rollout_bucket, rollout_admits, rollout_percent, ROLLOUT_BUCKETS};
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
        artifact_url: format!("{DOWNLOAD_ORIGIN}/release.tar.gz"),
        artifact_signature: "signature".into(),
        notes: None,
        rollout_eligibility: RolloutEligibility::Eligible,
    };
    c.release_id = c.identity();
    c
}
const NOW: u64 = 1_800_000_000;
fn offered(s: &mut NativeUpdateState, version: &str, automatic: bool, bucket: u8) {
    check::apply_offer(
        s,
        Ok(Some(feed::Offer {
            candidate: candidate(version),
            published_age_secs: Some(0),
        })),
        automatic,
        bucket,
        NOW,
    );
}
#[test]
fn staged_release_survives_network_failure_but_not_withdrawal() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 99);
    s.staged_release_id = s.candidate.as_ref().map(|c| c.release_id.clone());
    s.installation = Installation::Staged;
    check::apply_offer(&mut s, Err(Failure::CheckFailed.into()), true, 99, NOW);
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_restart_to_update);
    assert_eq!(s.offer_confirmed_at, Some(NOW));
    check::apply_offer(&mut s, Ok(None), true, 99, NOW);
    s.refresh_capabilities(true);
    assert!(!s.capabilities.can_restart_to_update);
    assert_eq!(s.installation, Installation::None);
    assert!(s.offer_confirmed_at.is_none());
}
#[test]
fn an_empty_feed_does_not_retract_an_installed_or_committed_release() {
    for installation in [Installation::AwaitingStartup, Installation::Committed] {
        let mut s = state();
        offered(&mut s, "1.1.0", false, 0);
        s.staged_release_id = s.candidate.as_ref().map(|c| c.release_id.clone());
        s.installation = installation;
        check::apply_offer(&mut s, Ok(None), true, 0, NOW);
        assert_eq!(s.installation, installation);
        assert!(s.candidate.is_some());
        assert_eq!(s.discovery, Discovery::UpToDate);
    }
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
fn a_committed_handoff_offers_restart_and_blocks_new_work() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 0);
    s.staged_release_id = s.candidate.as_ref().map(|c| c.release_id.clone());
    s.installation = Installation::Committed;
    s.automatic_updates_enabled = false;
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_restart_to_update);
    assert!(!s.capabilities.can_check);
    assert!(!s.capabilities.can_download);
    assert!(!s.capabilities.can_install_automatically);
}
#[test]
fn an_uncoordinated_process_cannot_install_and_says_why() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 0);
    s.coordinated = false;
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_check);
    assert!(!s.capabilities.can_download);
    assert_eq!(s.capabilities.blocked_reason.as_deref(), Some("state_unavailable"));
    s.refresh_capabilities(false);
    assert_eq!(
        s.capabilities.blocked_reason.as_deref(),
        Some("unsupported_installation")
    );
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
fn feed_endpoints_derive_from_the_embedded_key_generation() {
    let (generation, _) = feed::embedded_key();
    assert_eq!(
        UpdateChannel::Stable.endpoint(),
        format!("{DOWNLOAD_ORIGIN}/updates/stable/key-{generation}/latest.json")
    );
    assert_eq!(
        UpdateChannel::Preview.feed_name(),
        format!("latest-preview-key-{generation}.json")
    );
    assert!(!feed::feed_key().is_empty());
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
fn rollout_ramp_widens_with_manifest_age_and_treats_undated_releases_as_new() {
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
    assert!(rollout_admits(9, None));
    assert!(!rollout_admits(10, None));
}

#[test]
fn rollout_bucket_draw_stays_in_range() {
    for _ in 0..1000 {
        assert!(draw_rollout_bucket() < ROLLOUT_BUCKETS);
    }
}

#[test]
fn discovery_does_not_clear_an_installation_failure_for_the_same_release() {
    let mut s = state();
    offered(&mut s, "1.1.0", false, 0);
    s.installation = Installation::Failed;
    s.last_error = Some(Failure::VerificationFailed.into());
    offered(&mut s, "1.1.0", true, 0);
    assert_eq!(
        s.last_error.as_ref().unwrap().code,
        Failure::VerificationFailed
    );
    offered(&mut s, "1.2.0", true, 0);
    assert_eq!(s.installation, Installation::None);
    assert!(s.last_error.is_none());
}

#[test]
fn recovery_startup_can_discover_a_fix_without_confirming_the_installed_version() {
    let mut s = state();
    s.installation = Installation::AwaitingStartup;
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_check);
    offered(&mut s, "1.2.0", false, 0);
    s.refresh_capabilities(true);
    assert!(s.capabilities.can_download);
}

fn service_for_test() -> UpdateService {
    UpdateService {
        inner: tokio::sync::Mutex::new(Inner {
            state: state(),
            preferences: persistence::Preferences {
                format_version: 2,
                automatic_updates_enabled: true,
                channel: UpdateChannel::Stable,
                rollout_bucket: 0,
            },
            blocked_release: None,
            preferences_writable: true,
            manual_preparation: false,
        }),
        generation: AtomicU64::new(0),
        preparation_generation: AtomicU64::new(0),
        wake: tokio::sync::watch::channel(0).0,
        startup_ready: tokio::sync::watch::channel(false).0,
        engine_admission: std::sync::Arc::new(tokio::sync::RwLock::new(())),
        preparation: std::sync::Arc::new(tokio::sync::Mutex::new(())),
        activation: std::sync::Arc::new(tokio::sync::Mutex::new(())),
        _lease: None,
    }
}
#[tokio::test]
async fn cancelling_a_check_releases_discovery_before_a_new_check_can_start() {
    let service = service_for_test();
    let mut inner = service.inner.lock().await;
    inner.state.discovery = Discovery::Checking;
    service.cancel(&mut inner.state);
    assert_eq!(inner.state.discovery, Discovery::Idle);
    assert!(!service.current(0));
    inner.state.discovery = Discovery::Checking;
    assert!(service.current(1));
    assert!(!service.current(0));
}
#[tokio::test]
async fn scheduler_wakes_survive_the_gap_before_waiting_without_cancelling_preparation() {
    let service = service_for_test();
    let mut wake = service.wake.subscribe();
    service.wake_scheduler();
    tokio::time::timeout(Duration::from_millis(100), wake.changed())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(service.preparation_generation.load(Ordering::Acquire), 0);
    assert!(service.current(0));
}

#[tokio::test]
async fn closing_engine_admission_drains_existing_starts_and_blocks_new_ones() {
    let service = service_for_test();
    service.startup_ready.send_replace(true);
    let start = service.startup_admission().await;
    assert!(
        tokio::time::timeout(Duration::from_millis(10), service.close_startup())
            .await
            .is_err()
    );
    drop(start);
    service.close_startup().await;
    assert!(
        tokio::time::timeout(Duration::from_millis(10), service.startup_admission())
            .await
            .is_err()
    );
    service.startup_ready.send_replace(true);
    let _resumed = service.startup_admission().await;
}
