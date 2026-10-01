//! Native update lifecycle and restart journal.

pub(crate) mod check;
mod error;
pub(crate) mod install;
mod persistence;
pub(crate) mod preferences;
#[cfg(test)]
mod tests;

use self::check::{draw_rollout_bucket, embedded_key, rollout_admits};
use self::persistence::{detect_install_source, load_preferences, reconcile_journal};
pub use error::{UpdateError, UpdateErrorCode};
use serde::{Deserialize, Serialize};
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tauri::{AppHandle, Emitter};
use tauri_plugin_updater::Update;
use UpdateErrorCode as Failure;

static UPDATE_REVISION: AtomicU64 = AtomicU64::new(0);
const JOURNAL_FILE: &str = "update-state.json";
const JOURNAL_TMP: &str = "update-state.json.tmp";
const PREFERENCES_FILE: &str = "update-preferences.json";
const PREFERENCES_TMP: &str = "update-preferences.json.tmp";
const INSTALL_SOURCE_FILE: &str = "install-source.json";
const UPDATE_STATE_EVENT: &str = "update-state-changed";
const INITIAL_CHECK_DELAY: Duration = Duration::from_secs(30);
const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);
const RETRY_INTERVAL: Duration = Duration::from_secs(30 * 60);
const DISABLED_POLL_INTERVAL: Duration = Duration::from_secs(60 * 60);
const CHECK_REQUEST_TIMEOUT: Duration = Duration::from_secs(30);
const DOWNLOAD_REQUEST_TIMEOUT: Duration = Duration::from_secs(30 * 60);
const DOWNLOAD_ORIGIN: &str = "https://downloads.paintedwolf.dev";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum InstallSource {
    HomebrewCask,
    DirectDownload,
    Unknown,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum UpdatePhase {
    Idle,
    Checking,
    UpToDate,
    Available,
    HeldBack,
    Installing,
    RestartRequired,
    Unavailable,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum UpdateChannel {
    Stable,
    Preview,
}

impl UpdateChannel {
    fn endpoint(self) -> String {
        let channel = match self {
            Self::Stable => "stable",
            Self::Preview => "preview",
        };
        format!(
            "{DOWNLOAD_ORIGIN}/updates/{channel}/key-{}/latest.json",
            embedded_key().0
        )
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RolloutEligibility {
    NotApplicable,
    Eligible,
    HeldBack,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct NativeUpdateState {
    pub revision: u64,
    pub phase: UpdatePhase,
    pub current_version: String,
    pub channel: UpdateChannel,
    pub rollout_eligibility: RolloutEligibility,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub available_version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    pub install_source: InstallSource,
    pub checks_enabled: bool,
    pub downloaded_bytes: u64,
    pub total_bytes: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<UpdateError>,
}

impl NativeUpdateState {
    fn idle(
        current_version: String,
        install_source: InstallSource,
        channel: UpdateChannel,
    ) -> Self {
        Self {
            revision: 0,
            phase: UpdatePhase::Idle,
            current_version,
            channel,
            rollout_eligibility: RolloutEligibility::NotApplicable,
            available_version: None,
            notes: None,
            install_source,
            checks_enabled: true,
            downloaded_bytes: 0,
            total_bytes: None,
            error: None,
        }
    }
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
enum UpdateJournalPhase {
    Installing,
    RestartRequired,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct UpdateJournal {
    to_version: String,
    phase: UpdateJournalPhase,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct UpdatePreferences {
    checks_enabled: bool,
    channel: UpdateChannel,
    /// Local rollout bucket; not transmitted.
    rollout_bucket: u8,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct InstallSourceReceipt {
    install_source: InstallSourceReceiptKind,
    release_channel: UpdateChannel,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "snake_case")]
enum InstallSourceReceiptKind {
    HomebrewCask,
}

impl UpdatePreferences {
    fn new(channel: UpdateChannel, rollout_bucket: u8) -> Self {
        Self {
            checks_enabled: true,
            channel,
            rollout_bucket,
        }
    }
}

fn default_channel(current_version: &str) -> Result<UpdateChannel, UpdateError> {
    let version = semver::Version::parse(current_version)
        .map_err(|err| UpdateError::new(Failure::InvalidVersion, err))?;
    Ok(if version.pre.is_empty() {
        UpdateChannel::Stable
    } else {
        UpdateChannel::Preview
    })
}

struct Inner<C = Update> {
    state: NativeUpdateState,
    candidate: Option<C>,
    check_epoch: u64,
    rollout_bucket: u8,
}

struct CheckTicket {
    epoch: u64,
    automatic: bool,
    previous: NativeUpdateState,
}

struct CheckedCandidate<C> {
    candidate: C,
    current_version: String,
    version: String,
    notes: Option<String>,
    age_secs: Option<u64>,
}

impl<C> Inner<C> {
    fn reconcile_state(&mut self, dir: &Path, current_version: &str) {
        if !matches!(
            self.state.phase,
            UpdatePhase::Installing | UpdatePhase::RestartRequired
        ) {
            let previous_phase = self.state.phase;
            if let Err(err) = reconcile_journal(dir, current_version, &mut self.state) {
                self.state.error = Some(err);
            }
            if previous_phase == UpdatePhase::Checking && self.state.phase != previous_phase {
                self.check_epoch = self.check_epoch.wrapping_add(1);
            }
        }
        if !matches!(
            self.state.phase,
            UpdatePhase::Available | UpdatePhase::Checking | UpdatePhase::Installing
        ) {
            self.candidate = None;
        }
    }

    fn begin_check(&mut self, automatic: bool) -> Result<Option<CheckTicket>, UpdateError> {
        if automatic && !self.state.checks_enabled {
            return Ok(None);
        }
        if !check_transition_allowed(self.state.phase) {
            return if automatic {
                Ok(None)
            } else {
                Err(UpdateError::from(Failure::InvalidTransition))
            };
        }
        let previous = self.state.clone();
        self.state.phase = UpdatePhase::Checking;
        self.state.error = None;
        self.check_epoch = self.check_epoch.wrapping_add(1);
        Ok(Some(CheckTicket {
            epoch: self.check_epoch,
            automatic,
            previous,
        }))
    }

    fn set_checks_enabled(&mut self, enabled: bool) {
        self.state.checks_enabled = enabled;
        self.state.error = None;
        if !enabled {
            self.check_epoch = self.check_epoch.wrapping_add(1);
            if !matches!(
                self.state.phase,
                UpdatePhase::Installing | UpdatePhase::RestartRequired
            ) {
                self.state.phase = UpdatePhase::Idle;
                self.state.available_version = None;
                self.state.notes = None;
                self.state.rollout_eligibility = RolloutEligibility::NotApplicable;
                self.candidate = None;
            }
        }
    }

    fn set_channel(&mut self, channel: UpdateChannel) {
        if self.state.channel == channel {
            return;
        }
        self.check_epoch = self.check_epoch.wrapping_add(1);
        self.state.channel = channel;
        if self.state.phase == UpdatePhase::RestartRequired {
            return;
        }
        self.state.phase = UpdatePhase::Idle;
        self.state.available_version = None;
        self.state.notes = None;
        self.state.rollout_eligibility = RolloutEligibility::NotApplicable;
        self.state.error = None;
        self.candidate = None;
    }

    fn finish_install_check(
        &mut self,
        ticket: CheckTicket,
        checked: Result<Option<CheckedCandidate<C>>, UpdateError>,
        source: InstallSource,
        source_error: Option<UpdateError>,
        matches_displayed: impl FnOnce(&C) -> bool,
    ) -> bool {
        let eligible = checked
            .as_ref()
            .ok()
            .and_then(Option::as_ref)
            .is_some_and(|fresh| matches_displayed(&fresh.candidate))
            && source == InstallSource::DirectDownload
            && source_error.is_none();
        let completed = self.finish_check(ticket, checked, source, source_error);
        if !eligible && self.state.error.is_none() && self.state.phase == UpdatePhase::Available {
            self.state.error = Some(Failure::CandidateChanged.into());
        }
        eligible && completed
    }

    fn finish_check(
        &mut self,
        ticket: CheckTicket,
        checked: Result<Option<CheckedCandidate<C>>, UpdateError>,
        install_source: InstallSource,
        install_source_error: Option<UpdateError>,
    ) -> bool {
        // Only the current check epoch can complete; stale results are inert.
        if self.check_epoch != ticket.epoch || self.state.phase != UpdatePhase::Checking {
            return false;
        }
        let surfaced = ticket.previous.phase == UpdatePhase::Available && self.candidate.is_some();
        let mut state = NativeUpdateState::idle(
            self.state.current_version.clone(),
            install_source,
            self.state.channel,
        );
        state.checks_enabled = self.state.checks_enabled;
        state.error = install_source_error;
        match checked {
            Ok(Some(checked)) => {
                let already_surfaced = surfaced
                    && ticket.previous.available_version.as_deref()
                        == Some(checked.version.as_str());
                if !ticket.automatic
                    || already_surfaced
                    || rollout_admits(self.rollout_bucket, checked.age_secs)
                {
                    state.phase = UpdatePhase::Available;
                    state.current_version = checked.current_version;
                    state.available_version = Some(checked.version);
                    state.notes = checked.notes;
                    state.rollout_eligibility = RolloutEligibility::Eligible;
                    self.candidate = Some(checked.candidate);
                } else if surfaced {
                    // A newer release still in rollout does not withdraw an offered one.
                    state.phase = UpdatePhase::Available;
                    state.available_version = ticket.previous.available_version;
                    state.notes = ticket.previous.notes;
                    state.rollout_eligibility = RolloutEligibility::Eligible;
                } else {
                    state.phase = UpdatePhase::HeldBack;
                    state.available_version = Some(checked.version);
                    state.rollout_eligibility = RolloutEligibility::HeldBack;
                    self.candidate = None;
                }
            }
            Ok(None) => {
                // A successful manifest check can withdraw a release.
                state.phase = UpdatePhase::UpToDate;
                self.candidate = None;
            }
            Err(err) => {
                if surfaced {
                    state.phase = UpdatePhase::Available;
                    state.available_version = ticket.previous.available_version;
                    state.notes = ticket.previous.notes;
                    state.rollout_eligibility = RolloutEligibility::Eligible;
                } else {
                    state.phase = UpdatePhase::Unavailable;
                    self.candidate = None;
                }
                state.error = Some(match state.error {
                    Some(source_err) => source_err.with_context(err),
                    None => err,
                });
            }
        }
        self.state = state;
        true
    }
}

pub struct UpdateService {
    operation: tokio::sync::Mutex<()>,
    inner: tokio::sync::Mutex<Inner>,
}

impl UpdateService {
    pub fn new(current_version: String) -> Self {
        let version_channel = default_channel(&current_version)
            .expect("compiled product version must be valid SemVer");
        let (install_source, receipt_channel, install_source_error) = detect_install_source();
        let initial_channel = receipt_channel.unwrap_or(version_channel);
        let mut state = NativeUpdateState::idle(current_version, install_source, initial_channel);
        state.error = install_source_error;
        let mut rollout_bucket = draw_rollout_bucket();
        if let Some(dir) = crate::den_state_dir() {
            match load_preferences(&dir, initial_channel, rollout_bucket) {
                Ok(preferences) => {
                    state.checks_enabled = preferences.checks_enabled;
                    state.channel = preferences.channel;
                    rollout_bucket = preferences.rollout_bucket;
                }
                Err(err) => {
                    state.checks_enabled = false;
                    state.error = Some(err);
                }
            }
        }
        Self {
            operation: tokio::sync::Mutex::new(()),
            inner: tokio::sync::Mutex::new(Inner {
                state,
                candidate: None,
                check_epoch: 0,
                rollout_bucket,
            }),
        }
    }
}

fn emit_update_state(app: &AppHandle, state: &mut NativeUpdateState) {
    state.revision = UPDATE_REVISION.fetch_add(1, Ordering::Relaxed) + 1;
    let _ = app.emit(UPDATE_STATE_EVENT, &*state);
}

fn check_transition_allowed(phase: UpdatePhase) -> bool {
    !matches!(
        phase,
        UpdatePhase::Checking | UpdatePhase::Installing | UpdatePhase::RestartRequired
    )
}

fn install_transition_allowed(phase: UpdatePhase) -> bool {
    phase == UpdatePhase::Available
}
