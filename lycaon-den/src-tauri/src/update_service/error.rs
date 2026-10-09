//! Typed failures shared by native commands and update state events.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum UpdateErrorCode {
    StateUnavailable,
    InvalidVersion,
    InvalidTransition,
    CandidateMissing,
    CandidateChanged,
    PackageManaged,
    InstallSourceUnavailable,
    PreferencesUnavailable,
    JournalUnavailable,
    Interrupted,
    CheckFailed,
    FeedRejected,
    DownloadFailed,
    VerificationFailed,
    InstallFailed,
    InvalidRelease,
    Cancelled,
    DiskSpace,
    UnsupportedInstallation,
    EngineStopFailed,
    ReleaseWithdrawn,
    ActivationFailed,
    RecoveryRequired,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct UpdateError {
    pub code: UpdateErrorCode,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

impl From<UpdateErrorCode> for UpdateError {
    fn from(code: UpdateErrorCode) -> Self {
        Self { code, detail: None }
    }
}

impl UpdateError {
    pub fn new(code: UpdateErrorCode, detail: impl std::fmt::Display) -> Self {
        Self {
            code,
            detail: Some(detail.to_string()),
        }
    }

    pub fn with_context(mut self, context: impl std::fmt::Display) -> Self {
        self.detail = Some(match self.detail {
            Some(detail) => format!("{detail}; {context}"),
            None => context.to_string(),
        });
        self
    }

    /// Transport failures are retried; everything else about a feed response is a release defect.
    pub fn transport(error: reqwest::Error) -> Self {
        // A client error says the origin has no such release; everything else is transient.
        let code = if error
            .status()
            .is_some_and(|status| status.is_client_error())
        {
            UpdateErrorCode::InvalidRelease
        } else {
            UpdateErrorCode::CheckFailed
        };
        Self::new(code, error)
    }
}

impl std::fmt::Display for UpdateError {
    fn fmt(&self, out: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(out, "{:?}", self.code)?;
        if let Some(detail) = &self.detail {
            write!(out, ": {detail}")?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn errors_serialize_as_structured_command_and_event_values() {
        let value = serde_json::to_value(UpdateError::from(UpdateErrorCode::Interrupted))
            .expect("serialize update failure");
        assert_eq!(value, serde_json::json!({"code": "interrupted"}));
    }

    #[test]
    fn context_is_appended_to_existing_detail() {
        let error = UpdateError::new(UpdateErrorCode::DiskSpace, "first").with_context("second");
        assert_eq!(error.detail.as_deref(), Some("first; second"));
        assert_eq!(error.to_string(), "DiskSpace: first; second");
    }
}
