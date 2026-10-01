use super::logging::engine_failure_message;
use super::process::child_state;
use super::{ChildState, SidecarStartError, SidecarState};
use serde::{Deserialize, Serialize};
use std::io::{BufRead, BufReader, Read};
use std::process::ChildStdout;
use std::sync::atomic::Ordering;
use std::sync::mpsc;
use std::time::{Duration, Instant};

pub(super) const STARTUP_PROTOCOL_VERSION: u8 = 1;
pub(super) const STARTUP_RECORD_MAX_BYTES: usize = 16 * 1024;
pub(super) const STARTUP_RECV_POLL: Duration = Duration::from_millis(100);
pub(super) const STARTUP_STALL_AFTER: Duration = Duration::from_secs(5);
pub(super) const STARTUP_STALL_UPDATE: Duration = Duration::from_secs(1);
pub(super) const STORE_INSTANCE_LOCKED_EXIT: i32 = 69;

#[derive(Debug, Clone, Copy, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(super) enum StartupPhase {
    Launch,
    Observability,
    Store,
    UpgradeSnapshot,
    SchemaUpgrade,
    UpgradeValidation,
    Configuration,
    UserPath,
    Credentials,
    HostResources,
    Providers,
    Pricing,
    Tools,
    Agents,
    Sessions,
    Policy,
    Events,
    Workflows,
    Workers,
    Scan,
    Research,
    Grounding,
    Coordinator,
    Server,
    Services,
    Recovery,
    BackgroundWork,
    Binding,
    Ready,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(super) enum StartupKind {
    Phase,
    Heartbeat,
    Ready,
    Failed,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct StartupRecord {
    pub(super) protocol: u8,
    pub(super) sequence: u64,
    pub(super) pid: u32,
    pub(super) kind: StartupKind,
    pub(super) phase: StartupPhase,
    pub(super) elapsed_ms: u64,
    #[serde(default)]
    pub(super) port: u16,
    #[serde(default)]
    pub(super) code: String,
}

#[derive(Debug, Clone, Copy, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(super) enum StartupStatus {
    Starting,
    Stalled,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub(super) struct StartupProgress {
    pub(super) protocol: u8,
    pub(super) status: StartupStatus,
    pub(super) phase: StartupPhase,
    pub(super) pid: u32,
    pub(super) sequence: u64,
    pub(super) elapsed_ms: u64,
    pub(super) silence_ms: u64,
}

pub(super) fn read_bounded_startup_line<R: BufRead>(
    reader: &mut R,
) -> Result<Option<String>, String> {
    let mut bytes = Vec::with_capacity(512);
    loop {
        let available = reader
            .fill_buf()
            .map_err(|err| format!("read engine startup protocol: {err}"))?;
        if available.is_empty() {
            return if bytes.is_empty() {
                Ok(None)
            } else {
                Err("engine closed the startup protocol mid-record".into())
            };
        }
        let newline = available.iter().position(|byte| *byte == b'\n');
        let take = newline.map_or(available.len(), |index| index + 1);
        if bytes.len() + take > STARTUP_RECORD_MAX_BYTES {
            return Err(format!(
                "engine startup record exceeded {STARTUP_RECORD_MAX_BYTES} bytes"
            ));
        }
        bytes.extend_from_slice(&available[..take]);
        reader.consume(take);
        if newline.is_some() {
            return String::from_utf8(bytes)
                .map(Some)
                .map_err(|err| format!("engine startup record is not UTF-8: {err}"));
        }
    }
}

pub(super) fn read_startup_records<R: Read>(
    stdout: R,
    tx: mpsc::Sender<Result<StartupRecord, String>>,
) {
    let mut reader = BufReader::new(stdout);
    loop {
        match read_bounded_startup_line(&mut reader) {
            Ok(None) => {
                let _ = tx.send(Err("engine closed the startup protocol before ready".into()));
                return;
            }
            Ok(Some(line)) => match serde_json::from_str::<StartupRecord>(&line) {
                Ok(record) => {
                    let terminal = matches!(record.kind, StartupKind::Ready | StartupKind::Failed);
                    if tx.send(Ok(record)).is_err() {
                        return;
                    }
                    if terminal {
                        return;
                    }
                }
                Err(err) => {
                    let _ = tx.send(Err(format!("invalid engine startup record: {err}")));
                    return;
                }
            },
            Err(err) => {
                let _ = tx.send(Err(err));
                return;
            }
        }
    }
}

pub(super) fn validate_startup_record(
    record: &StartupRecord,
    child_pid: u32,
    previous_sequence: u64,
) -> Result<(), String> {
    if record.protocol != STARTUP_PROTOCOL_VERSION {
        return Err(format!(
            "engine startup protocol version {} is unsupported",
            record.protocol
        ));
    }
    if record.pid != child_pid {
        return Err(format!(
            "engine startup record pid {} does not match child {child_pid}",
            record.pid
        ));
    }
    if record.sequence != previous_sequence + 1 {
        return Err(format!(
            "engine startup sequence {} followed {previous_sequence}",
            record.sequence
        ));
    }
    match record.kind {
        StartupKind::Ready => {
            if record.phase != StartupPhase::Ready || record.port == 0 {
                return Err("engine ready record has no usable port".into());
            }
            if !record.code.is_empty() {
                return Err("engine ready record carries a failure code".into());
            }
        }
        StartupKind::Failed => {
            if record.code.trim().is_empty() {
                return Err("engine failure record has no code".into());
            }
            if record.phase == StartupPhase::Ready || record.port != 0 {
                return Err("engine failure record carries ready fields".into());
            }
        }
        StartupKind::Phase | StartupKind::Heartbeat => {
            if record.phase == StartupPhase::Ready || record.port != 0 || !record.code.is_empty() {
                return Err("engine progress record carries terminal fields".into());
            }
        }
    }
    Ok(())
}

pub(super) fn startup_exit_error(code: Option<i32>) -> SidecarStartError {
    if code == Some(STORE_INSTANCE_LOCKED_EXIT) {
        return SidecarStartError::StoreLocked(engine_failure_message(
            "another engine is already serving this store".to_string(),
        ));
    }
    SidecarStartError::Failed(engine_failure_message(format!(
        "engine exited before reporting ready (status {})",
        code.map_or_else(|| "unavailable".to_string(), |value| value.to_string())
    )))
}

pub(super) fn await_child_ready<F>(
    state: &SidecarState,
    child_pid: u32,
    stdout: ChildStdout,
    emit: &F,
) -> Result<u16, SidecarStartError>
where
    F: Fn(StartupProgress),
{
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || read_startup_records(stdout, tx));

    let started = Instant::now();
    let mut last_record_at = started;
    let mut phase = StartupPhase::Launch;
    let mut sequence = 0;
    let mut last_stall_update: Option<Instant> = None;
    loop {
        if state.cancel_start.load(Ordering::Acquire) {
            return Err(SidecarStartError::Cancelled(
                "Engine startup was stopped.".into(),
            ));
        }
        match rx.recv_timeout(STARTUP_RECV_POLL) {
            Ok(Err(err)) => {
                if let ChildState::Exited(code) =
                    child_state(state.process.lock().unwrap().as_mut())
                {
                    return Err(startup_exit_error(code));
                }
                return Err(SidecarStartError::Failed(engine_failure_message(err)));
            }
            Ok(Ok(record)) => {
                if let Err(err) = validate_startup_record(&record, child_pid, sequence) {
                    return Err(SidecarStartError::Failed(engine_failure_message(err)));
                }
                sequence = record.sequence;
                phase = record.phase;
                last_record_at = Instant::now();
                last_stall_update = None;
                match record.kind {
                    StartupKind::Failed => {
                        let failure = match record.code.as_str() {
                            "store_locked" => {
                                SidecarStartError::StoreLocked(engine_failure_message(
                                    "another engine is already serving this store".into(),
                                ))
                            }
                            "credential_vault_locked" => SidecarStartError::CredentialVaultLocked(
                                "Enter your app password to unlock saved credentials.".into(),
                            ),
                            "credential_vault_uninitialized" => {
                                SidecarStartError::CredentialVaultUninitialized(
                                    "Create an app password to protect saved credentials.".into(),
                                )
                            }
                            "credential_vault_unlock_failed" => {
                                SidecarStartError::CredentialVaultUnlockFailed(
                                    "That password did not unlock the credential vault.".into(),
                                )
                            }
                            "credential_vault_corrupt" => {
                                SidecarStartError::CredentialVaultCorrupt(engine_failure_message(
                                    "the encrypted credential vault or its identity is damaged"
                                        .into(),
                                ))
                            }
                            _ => SidecarStartError::Failed(engine_failure_message(format!(
                                "engine reported startup failure {} during {:?}",
                                record.code, record.phase
                            ))),
                        };
                        return Err(failure);
                    }
                    StartupKind::Ready => {
                        return Ok(record.port);
                    }
                    StartupKind::Phase | StartupKind::Heartbeat => {
                        emit(StartupProgress {
                            protocol: STARTUP_PROTOCOL_VERSION,
                            status: StartupStatus::Starting,
                            phase,
                            pid: child_pid,
                            sequence,
                            elapsed_ms: record.elapsed_ms,
                            silence_ms: 0,
                        });
                    }
                }
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                if let ChildState::Exited(code) =
                    child_state(state.process.lock().unwrap().as_mut())
                {
                    return Err(startup_exit_error(code));
                }
                return Err(SidecarStartError::Failed(engine_failure_message(
                    "engine startup protocol disconnected before ready".into(),
                )));
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {
                if let ChildState::Exited(code) =
                    child_state(state.process.lock().unwrap().as_mut())
                {
                    return Err(startup_exit_error(code));
                }
                let silence = last_record_at.elapsed();
                let update_due = last_stall_update
                    .is_none_or(|last_update| last_update.elapsed() >= STARTUP_STALL_UPDATE);
                if silence >= STARTUP_STALL_AFTER && update_due {
                    last_stall_update = Some(Instant::now());
                    emit(StartupProgress {
                        protocol: STARTUP_PROTOCOL_VERSION,
                        status: StartupStatus::Stalled,
                        phase,
                        pid: child_pid,
                        sequence,
                        elapsed_ms: started.elapsed().as_millis() as u64,
                        silence_ms: silence.as_millis() as u64,
                    });
                }
            }
        }
    }
}
