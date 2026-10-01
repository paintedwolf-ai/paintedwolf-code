//! Typed failures shared by native commands and update state events.

use serde::Serialize;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
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
    DownloadFailed,
    VerificationFailed,
    InstallFailed,
    InvalidRelease,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
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

    pub fn check(error: tauri_plugin_updater::Error) -> Self {
        use tauri_plugin_updater::Error;
        let code = match &error {
            Error::Reqwest(_) | Error::Network(_) | Error::ReleaseNotFound => {
                UpdateErrorCode::CheckFailed
            }
            _ => UpdateErrorCode::InvalidRelease,
        };
        Self::new(code, error)
    }

    pub fn download(error: tauri_plugin_updater::Error) -> Self {
        use tauri_plugin_updater::Error;
        let code = match &error {
            Error::Minisign(_) | Error::Base64(_) | Error::SignatureUtf8(_) => {
                UpdateErrorCode::VerificationFailed
            }
            _ => UpdateErrorCode::DownloadFailed,
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
    fn download_failures_use_typed_causes() {
        let signature = UpdateError::download(tauri_plugin_updater::Error::SignatureUtf8(
            "opaque signature diagnostic".into(),
        ));
        assert_eq!(signature.code, UpdateErrorCode::VerificationFailed);
        let transport = UpdateError::download(tauri_plugin_updater::Error::Network(
            "signature verification failed".into(),
        ));
        assert_eq!(transport.code, UpdateErrorCode::DownloadFailed);
    }

    #[test]
    fn check_errors_separate_transport_from_invalid_release_metadata() {
        assert_eq!(
            UpdateError::check(tauri_plugin_updater::Error::ReleaseNotFound).code,
            UpdateErrorCode::CheckFailed
        );
        assert_eq!(
            UpdateError::check(tauri_plugin_updater::Error::TargetNotFound("test".into())).code,
            UpdateErrorCode::InvalidRelease
        );
    }

    #[test]
    fn errors_serialize_as_structured_command_and_event_values() {
        let value = serde_json::to_value(UpdateError::from(UpdateErrorCode::Interrupted))
            .expect("serialize update failure");
        assert_eq!(value, serde_json::json!({"code": "interrupted"}));
    }
}
