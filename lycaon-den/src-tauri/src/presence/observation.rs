//! Bounded public confirmation metadata; no record authorizes input or host release.

use super::{ConfirmationChallenge, PresenceCommandError};
use serde_json::{json, Value};
use std::io::Write;

fn challenge_document(binding: &ConfirmationChallenge<'_>) -> Option<Value> {
    if binding.challenge_id.len() != 36
        || uuid::Uuid::parse_str(binding.challenge_id).ok()?.to_string() != binding.challenge_id
        || !matches!(binding.purpose, "reveal" | "unlock")
        || binding.window_label.is_empty() || binding.window_label.len() > 128 {
        return None;
    }
    Some(json!({
        "challenge_id": binding.challenge_id, "purpose": binding.purpose,
        "window_label": binding.window_label, "generation": binding.step_away_generation,
    }))
}

fn confirmation_document(binding: &ConfirmationChallenge<'_>, authenticator: Option<&str>,
    error: Option<&PresenceCommandError>, invalidated: bool) -> Option<Value> {
    let mut row = challenge_document(binding)?;
    let (status, code, resolution) = match (authenticator, error, invalidated) {
        (Some("macos_user_presence" | "windows_user_presence" | "linux_user_presence"), None, false) =>
            ("confirmed", None, None),
        (None, Some(error), stale) if matches!(error.code.as_str(),
            "presence_unavailable" | "presence_denied" | "presence_failed")
            && (!stale || error.code == "presence_denied") =>
            ("refused", Some(error.code.as_str()), if stale { Some("step_away") } else { None }),
        _ => return None,
    };
    row["format_version"] = json!(1);
    row["status"] = json!(status);
    row["authenticator"] = json!(authenticator);
    row["code"] = json!(code);
    row["resolution"] = json!(resolution);
    Some(row)
}

fn emit(prefix: &str, row: &Value) {
    // A diagnostic sink failure must never change authentication or cleanup.
    let _ = writeln!(std::io::stderr().lock(), "{prefix}{row}");
}

pub(super) fn challenge(binding: &ConfirmationChallenge<'_>) {
    if let Some(row) = challenge_document(binding) {
        emit("Step-away presence challenge: ", &row);
    }
}

pub(super) fn confirmed(binding: &ConfirmationChallenge<'_>, authenticator: &str) {
    if let Some(row) = confirmation_document(binding, Some(authenticator), None, false) {
        emit("Native presence confirmation: ", &row);
    }
}

pub(super) fn refused(binding: &ConfirmationChallenge<'_>, error: &PresenceCommandError, invalidated: bool) {
    if let Some(row) = confirmation_document(binding, None, Some(error), invalidated) {
        emit("Native presence confirmation: ", &row);
    }
}

fn invalidation_document(binding: &ConfirmationChallenge<'_>, after: u64) -> Option<Value> {
    let mut row = challenge_document(binding)?;
    if after <= binding.step_away_generation { return None; }
    row.as_object_mut()?.remove("generation");
    row["generation_before"] = json!(binding.step_away_generation);
    row["generation_after"] = json!(after);
    row["code"] = json!("presence_denied");
    row["resolution"] = json!("step_away");
    Some(row)
}

pub(super) fn invalidated(binding: &ConfirmationChallenge<'_>, after: u64) {
    if let Some(row) = invalidation_document(binding, after) {
        emit("Step-away presence invalidation: ", &row);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn binding(purpose: &str) -> ConfirmationChallenge<'_> {
        ConfirmationChallenge { challenge_id: "10000000-0000-4000-8000-000000000001",
            purpose, window_label: "main", step_away_generation: 7 }
    }

    #[test]
    fn native_outcome_has_exact_public_fields_for_both_purposes_and_all_authenticators() {
        for purpose in ["reveal", "unlock"] {
            let challenge = binding(purpose);
            for authenticator in ["macos_user_presence", "windows_user_presence", "linux_user_presence"] {
                let row = confirmation_document(&challenge, Some(authenticator), None, false).unwrap();
                assert_eq!(row, json!({"format_version":1, "challenge_id":challenge.challenge_id,
                    "purpose":purpose, "window_label":"main", "generation":7, "status":"confirmed",
                    "authenticator":authenticator, "code":null, "resolution":null}));
                assert!(row.get("host_release_verified").is_none());
            }
        }
    }

    #[test]
    fn refusal_never_serializes_diagnostic_prose_or_error_extras() {
        for error in [PresenceCommandError::unavailable("PRIVATE PASSWORD AND COOKIE"),
            PresenceCommandError::denied("PRIVATE PROOF AND BEARER"),
            PresenceCommandError::failed("PRIVATE SIGNATURE AND VALUE")] {
            let mut error = error.with_suggested_action("PRIVATE ACTION");
            error.actions = vec!["PRIVATE ACTION".into()];
            error.resolution = Some("PRIVATE RESOLUTION".into());
            let row = confirmation_document(&binding("reveal"), None, Some(&error), false).unwrap();
            assert_eq!(row["code"], error.code);
            assert_eq!(row["status"], "refused");
            assert!(row["authenticator"].is_null());
            assert!(row["resolution"].is_null());
            assert!(!row.to_string().contains("PRIVATE"));
        }
    }

    #[test]
    fn actual_epoch_refusal_is_distinct_from_authentication_and_public_error() {
        let error = PresenceCommandError::denied("private diagnostic");
        let row = confirmation_document(&binding("unlock"), None, Some(&error), true).unwrap();
        assert_eq!(row["resolution"], "step_away");
        assert!(error.resolution.is_none());
        assert_eq!(invalidation_document(&binding("unlock"), 9).unwrap(), json!({
            "challenge_id":binding("unlock").challenge_id, "purpose":"unlock", "window_label":"main",
            "generation_before":7, "generation_after":9, "code":"presence_denied", "resolution":"step_away"}));
        for epoch in [0, 7] { assert!(invalidation_document(&binding("unlock"), epoch).is_none()); }
    }

    #[test]
    fn malformed_unbounded_or_contradictory_metadata_is_not_emitted() {
        let mut invalid = binding("reveal");
        invalid.challenge_id = "private payload";
        assert!(challenge_document(&invalid).is_none());
        invalid = binding("unknown");
        assert!(challenge_document(&invalid).is_none());
        let long = "x".repeat(129);
        invalid = binding("reveal");
        invalid.window_label = &long;
        assert!(challenge_document(&invalid).is_none());
        let mut error = PresenceCommandError::denied("private");
        error.code = "PRIVATE ERROR".into();
        assert!(confirmation_document(&binding("reveal"), None, Some(&error), false).is_none());
        assert!(confirmation_document(&binding("reveal"), Some("linux_app_password"), None, false).is_none());
        assert!(confirmation_document(&binding("reveal"), Some("linux_user_presence"), Some(&error), false).is_none());
        assert!(confirmation_document(&binding("reveal"), Some("linux_user_presence"), None, true).is_none());
    }
}
