//! Ephemeral correlation for an actual Polkit call; these records never authorize input or reveal.

use std::collections::HashMap;
use gtk::{gio, glib};
use super::super::ConfirmationChallenge;

#[derive(Clone, Copy, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub(super) enum Phase { Probe, Interactive }


fn document(
    challenge: &ConfirmationChallenge<'_>, subject: &HashMap<String, glib::Variant>,
    action: &str, authority: &str, request_id: &str, phase: Phase,
    sender: Option<&str>, result: Option<&Result<glib::Variant, glib::Error>>,
) -> Option<serde_json::Value> {
    // Refuse an unbounded observation, without changing the authentication result.
    if [(challenge.challenge_id, 64), (challenge.purpose, 64),
        (challenge.window_label, 64), (action, 256), (authority, 64),
        (request_id, 64)].iter().any(|(value, bound)| value.len() > *bound)
        || sender.is_some_and(|value| value.len() > 64) {
        return None;
    }
    let (transport, authorization) = match result {
        None => ("pending", None),
        Some(Ok(reply)) => ("returned", super::authorization_flags(reply)),
        Some(Err(error)) if error.matches(gio::IOErrorEnum::TimedOut) => ("timed_out", None),
        Some(Err(_)) => ("failed", None),
    };
    Some(serde_json::json!({
        "format_version": 1, "challenge": challenge, "action_id": action,
        "authority": authority, "sender": sender, "request_id": request_id,
        "phase": phase, "transport": transport,
        "subject": {
            "pid": subject.get("pid").and_then(|value| value.get::<u32>()),
            "start_time_ticks": subject.get("start-time").and_then(|value| value.get::<u64>()),
            "uid": subject.get("uid").and_then(|value| value.get::<i32>()),
        },
        "authorization": authorization.map(|(authorized, challenge)|
            serde_json::json!({"authorized": authorized, "challenge": challenge})),
    }))
}

pub(super) fn call(
    challenge: &ConfirmationChallenge<'_>, subject: &HashMap<String, glib::Variant>,
    action: &str, authority: &str, request_id: &str, phase: Phase,
    bus: &gio::DBusConnection,
    invoke: impl FnOnce() -> Result<glib::Variant, glib::Error>,
) -> Result<glib::Variant, glib::Error> {
    let sender = bus.unique_name();
    if let Some(record) = document(challenge, subject, action, authority,
        request_id, phase, sender.as_deref(), None) {
        eprintln!("Native polkit attempt: {record}");
    }
    let result = invoke();
    if let Some(record) = document(challenge, subject, action, authority,
        request_id, phase, sender.as_deref(), Some(&result)) {
        eprintln!("Native polkit attempt: {record}");
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use gtk::glib::prelude::*;

    #[test]
    fn correlation_has_only_bound_public_facts_and_never_failure_messages() {
        let challenge = ConfirmationChallenge { challenge_id: "challenge-id", purpose: "reveal",
            window_label: "main", step_away_generation: 7 };
        let subject = HashMap::from([
            ("pid".into(), 43u32.to_variant()),
            ("start-time".into(), 91u64.to_variant()),
            ("uid".into(), 30059i32.to_variant()),
            ("cookie".into(), "private-cookie-password".to_variant()),
        ]);
        let pending = document(&challenge, &subject, "dev.paintedwolf.code.confirm-presence",
            ":1.2", "request-id", Phase::Interactive, Some(":1.3"), None).unwrap();
        assert_eq!(pending["challenge"]["step_away_generation"], 7);
        assert_eq!(pending["subject"], serde_json::json!({"pid":43,"start_time_ticks":91,"uid":30059}));
        assert_eq!(pending["transport"], "pending");
        assert!(pending["authorization"].is_null());
        let failed = Err(glib::Error::new(gio::IOErrorEnum::Failed, "private-cookie-password"));
        let final_record = document(&challenge, &subject, "dev.paintedwolf.code.confirm-presence",
            ":1.2", "request-id", Phase::Interactive, Some(":1.3"), Some(&failed)).unwrap();
        assert_eq!(final_record["transport"], "failed");
        assert!(!final_record.to_string().contains("private-cookie-password"));
        assert!(final_record.get("proof_payload").is_none());
    }

    #[test]
    fn returned_authorization_and_timeout_are_distinct_from_host_release() {
        let challenge = ConfirmationChallenge { challenge_id: "challenge-id", purpose: "reveal",
            window_label: "main", step_away_generation: 7 };
        let subject = HashMap::new();
        let reply = Ok(((true, false, HashMap::<String, String>::new()),).to_variant());
        let returned = document(&challenge, &subject, "action", ":1.2", "request", Phase::Interactive,
            None, Some(&reply)).unwrap();
        assert_eq!(returned["authorization"], serde_json::json!({"authorized":true,"challenge":false}));
        assert_eq!(returned["transport"], "returned");
        assert!(returned.get("host_release").is_none());
        let timeout = Err(glib::Error::new(gio::IOErrorEnum::TimedOut, "private"));
        let expired = document(&challenge, &subject, "action", ":1.2", "request", Phase::Interactive,
            None, Some(&timeout)).unwrap();
        assert_eq!(expired["transport"], "timed_out");
        assert!(expired["authorization"].is_null());
    }
    #[test]
    fn oversized_context_is_omitted_instead_of_truncated_into_another_identity() {
        let id = "x".repeat(65);
        let challenge = ConfirmationChallenge { challenge_id: &id, purpose: "reveal",
            window_label: "main", step_away_generation: 7 };
        assert!(document(&challenge, &HashMap::new(), "action", ":1.2", "request",
            Phase::Probe, None, None).is_none());
    }

}
