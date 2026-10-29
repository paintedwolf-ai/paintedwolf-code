//! Public metadata from an actual validated failed startup, never the startup frame.

use super::{validate_startup_record, StartupKind, StartupRecord};
use serde_json::{json, Value};
use std::io::Write;

const PREFIX: &str = "Native startup outcome: ";

fn document(record: &StartupRecord, child_pid: u32, previous_sequence: u64) -> Option<Value> {
    if record.kind != StartupKind::Failed || child_pid == 0
        || previous_sequence.checked_add(1) != Some(record.sequence) {
        return None;
    }
    // Reuse the startup owner's validator; observing never introduces another policy.
    validate_startup_record(record, child_pid, previous_sequence).ok()?;
    let code = match record.code.as_str() {
        "store_locked" => "store_locked",
        "credential_vault_custodian_absent" => "credential_vault_custodian_absent",
        "credential_vault_custodian_unavailable" => "credential_vault_custodian_unavailable",
        "credential_vault_launcher_refused" => "credential_vault_launcher_refused",
        "credential_vault_identity_missing" => "credential_vault_identity_missing",
        "credential_vault_corrupt" => "credential_vault_corrupt",
        "build_failed" => "build_failed",
        _ => return None,
    };
    Some(json!({
        "format_version": 1, "protocol": record.protocol, "pid": record.pid,
        "sequence": record.sequence, "kind": "failed", "phase": record.phase,
        "elapsed_ms": record.elapsed_ms, "code": code,
    }))
}

fn write_failed(writer: &mut impl Write, record: &StartupRecord, child_pid: u32, previous_sequence: u64) {
    if let Some(row) = document(record, child_pid, previous_sequence) {
        // An unavailable diagnostic sink cannot change startup failure or cleanup.
        let _ = writeln!(writer, "{PREFIX}{row}");
    }
}

pub(super) fn failed(record: &StartupRecord, child_pid: u32, previous_sequence: u64) {
    write_failed(&mut std::io::stderr().lock(), record, child_pid, previous_sequence);
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sidecar::startup::StartupPhase;

    fn failed(code: &str) -> StartupRecord {
        StartupRecord { protocol: 1, sequence: 2, pid: 4242, kind: StartupKind::Failed,
            phase: StartupPhase::Credentials, elapsed_ms: 17, port: 0, code: code.into() }
    }

    #[test]
    fn failed_terminal_serializes_only_the_exact_public_schema_and_known_codes() {
        for code in ["store_locked", "credential_vault_custodian_absent",
            "credential_vault_custodian_unavailable", "credential_vault_launcher_refused",
            "credential_vault_identity_missing", "credential_vault_corrupt", "build_failed"] {
            let record = failed(code);
            let row = document(&record, 4242, 1).unwrap();
            assert_eq!(row, json!({"format_version":1, "protocol":1, "pid":4242, "sequence":2,
                "kind":"failed", "phase":"credentials", "elapsed_ms":17, "code":code}));
            let mut bytes = Vec::new();
            write_failed(&mut bytes, &record, 4242, 1);
            let output = String::from_utf8(bytes).unwrap();
            assert_eq!(output.lines().count(), 1);
            assert!(output.starts_with(PREFIX));
            assert_eq!(serde_json::from_str::<Value>(output.trim_end().strip_prefix(PREFIX).unwrap()).unwrap(), row);
        }
    }

    #[test]
    fn unvalidated_pid_sequence_protocol_and_terminal_shape_never_reach_the_sink() {
        for (child_pid, previous) in [(7, 1), (4242, 0), (0, 1), (4242, u64::MAX)] {
            let mut bytes = Vec::new();
            write_failed(&mut bytes, &failed("build_failed"), child_pid, previous);
            assert!(bytes.is_empty());
        }
        let mut wrong_protocol = failed("build_failed"); wrong_protocol.protocol = 2;
        let mut ready_phase = failed("build_failed"); ready_phase.phase = StartupPhase::Ready;
        let mut with_port = failed("build_failed"); with_port.port = 64421;
        for record in [wrong_protocol, ready_phase, with_port] {
            let mut bytes = Vec::new();
            write_failed(&mut bytes, &record, 4242, 1);
            assert!(bytes.is_empty());
        }
        let mut ready = failed("");
        ready.kind = StartupKind::Ready;
        ready.phase = StartupPhase::Ready;
        ready.port = 64421;
        let mut phase = failed(""); phase.kind = StartupKind::Phase;
        let mut heartbeat = failed(""); heartbeat.kind = StartupKind::Heartbeat;
        for record in [ready, phase, heartbeat] {
            validate_startup_record(&record, 4242, 1).unwrap();
            let mut bytes = Vec::new();
            write_failed(&mut bytes, &record, 4242, 1);
            assert!(bytes.is_empty());
        }
    }

    #[test]
    fn unknown_empty_secret_or_prose_code_is_not_copied_into_observations() {
        for code in ["", "future_failure", "PRIVATE OS PASSWORD", "credential_vault_corrupt PRIVATE VALUE",
            "credential_vault_custodian_absent\nPRIVATE BEARER", &"PRIVATE".repeat(4096)] {
            let mut bytes = Vec::new();
            write_failed(&mut bytes, &failed(code), 4242, 1);
            assert!(bytes.is_empty());
        }
    }

    #[test]
    fn sink_failure_is_ignored_without_mutating_the_original_typed_failure() {
        struct BrokenSink(usize);
        impl Write for BrokenSink {
            fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
                self.0 += 1;
                Err(std::io::Error::new(std::io::ErrorKind::BrokenPipe, "private sink diagnostic"))
            }
            fn flush(&mut self) -> std::io::Result<()> { Ok(()) }
        }
        let record = failed("credential_vault_custodian_absent");
        let mut sink = BrokenSink(0);
        write_failed(&mut sink, &record, 4242, 1);
        assert_eq!(sink.0, 1);
        assert_eq!(record.code, "credential_vault_custodian_absent");
        assert_eq!(record.sequence, 2);
        validate_startup_record(&record, 4242, 1).unwrap();
    }
}
