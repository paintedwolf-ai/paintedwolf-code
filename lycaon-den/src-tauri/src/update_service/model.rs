//! Native facts shared by discovery, preparation, and activation.
use super::UpdateError;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum InstallSource {
    HomebrewCask,
    DirectDownload,
    Unknown,
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum UpdateChannel {
    Stable,
    Preview,
}
impl UpdateChannel {
    pub fn endpoint(self) -> String {
        let channel = match self {
            Self::Stable => "stable",
            Self::Preview => "preview",
        };
        format!(
            "https://downloads.paintedwolf.dev/updates/{channel}/key-{}/latest.json",
            super::check::embedded_key().0
        )
    }
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Discovery {
    Idle,
    Checking,
    UpToDate,
    Available,
    HeldBack,
    Failed,
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Installation {
    None,
    Downloading,
    Verifying,
    Preparing,
    Staged,
    AwaitingExit,
    AwaitingStartup,
    Failed,
}
impl Installation {
    pub fn busy(self) -> bool {
        matches!(
            self,
            Self::Downloading | Self::Verifying | Self::Preparing | Self::AwaitingExit
        )
    }
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RolloutEligibility {
    Eligible,
    HeldBack,
}
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Candidate {
    pub release_id: String,
    pub version: String,
    pub channel: UpdateChannel,
    pub platform: String,
    pub signing_generation: u64,
    pub artifact_url: String,
    pub artifact_signature: String,
    pub notes: Option<String>,
    pub rollout_eligibility: RolloutEligibility,
}
impl Candidate {
    pub fn identity(&self) -> String {
        let identity = serde_json::to_vec(&(
            self.version.as_str(),
            self.channel,
            self.platform.as_str(),
            self.signing_generation,
            self.artifact_url.as_str(),
            self.artifact_signature.as_str(),
        ))
        .expect("release identity");
        format!("{:x}", Sha256::digest(identity))
    }
    pub fn valid_identity(&self) -> bool {
        self.release_id == self.identity()
    }
}
#[derive(Debug, Clone, Default, Serialize)]
pub struct Capabilities {
    pub can_check: bool,
    pub can_download: bool,
    pub can_restart_to_update: bool,
    pub can_install_automatically: bool,
    pub blocked_reason: Option<String>,
}
#[derive(Debug, Clone, Serialize)]
pub struct NativeUpdateState {
    pub service_instance_id: String,
    pub revision: u64,
    pub running_version: String,
    pub channel: UpdateChannel,
    pub automatic_updates_enabled: bool,
    pub startup_pending: bool,
    pub install_source: InstallSource,
    pub capabilities: Capabilities,
    pub discovery: Discovery,
    pub candidate: Option<Candidate>,
    pub installation: Installation,
    pub staged_release_id: Option<String>,
    pub downloaded_bytes: u64,
    pub total_bytes: Option<u64>,
    pub last_check_at: Option<u64>,
    pub next_check_at: Option<u64>,
    pub last_error: Option<UpdateError>,
}
impl NativeUpdateState {
    pub fn new(
        running_version: String,
        channel: UpdateChannel,
        install_source: InstallSource,
    ) -> Self {
        Self {
            service_instance_id: uuid::Uuid::new_v4().to_string(),
            revision: 0,
            running_version,
            channel,
            automatic_updates_enabled: true,
            startup_pending: true,
            install_source,
            capabilities: Capabilities::default(),
            discovery: Discovery::Idle,
            candidate: None,
            installation: Installation::None,
            staged_release_id: None,
            downloaded_bytes: 0,
            total_bytes: None,
            last_check_at: None,
            next_check_at: None,
            last_error: None,
        }
    }
    pub fn refresh_capabilities(&mut self, supported: bool) {
        let direct = self.install_source == InstallSource::DirectDownload;
        let ready = self.installation == Installation::Staged
            && self
                .candidate
                .as_ref()
                .is_some_and(|c| Some(&c.release_id) == self.staged_release_id.as_ref());
        self.capabilities = Capabilities {
            can_check: !self.installation.busy() && self.discovery != Discovery::Checking,
            can_download: self.discovery != Discovery::Checking
                && supported
                && direct
                && self
                    .candidate
                    .as_ref()
                    .is_some_and(|c| c.rollout_eligibility == RolloutEligibility::Eligible)
                && !self.installation.busy()
                && !ready,
            can_restart_to_update: supported && direct && ready,
            can_install_automatically: supported
                && direct
                && ready
                && self.automatic_updates_enabled,
            blocked_reason: if !direct {
                Some(
                    match self.install_source {
                        InstallSource::HomebrewCask => "package_managed",
                        _ => "install_source_unavailable",
                    }
                    .into(),
                )
            } else if !supported {
                Some("unsupported_installation".into())
            } else {
                None
            },
        };
    }
}
pub fn now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}
