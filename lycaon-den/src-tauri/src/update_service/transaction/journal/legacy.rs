//! The released format is decoded independently of the current journal.
use super::{Candidate, Failure, Phase, Transaction, UpdateError};
use serde::Deserialize;
use std::path::PathBuf;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct LegacyTransaction {
    format_version: u8,
    id: String,
    candidate: Candidate,
    target: PathBuf,
    previous_hash: String,
    next_hash: String,
    next_bundle_hash: String,
    phase: Phase,
    relaunch: bool,
    recovery_relaunch_attempted: bool,
    error: Option<String>,
}

pub(super) fn decode(value: serde_json::Value) -> Result<Transaction, UpdateError> {
    let old: LegacyTransaction = serde_json::from_value(value)
        .map_err(|error| UpdateError::new(Failure::JournalUnavailable, error))?;
    if old.format_version != 1 {
        return Err(Failure::JournalUnavailable.into());
    }
    Ok(Transaction {
        format_version: 2,
        prepared_bundle: super::super::installer::legacy_prepared_path(
            &old.target,
            &old.candidate,
        )?,
        id: old.id,
        candidate: old.candidate,
        target: old.target,
        previous_hash: old.previous_hash,
        next_hash: old.next_hash,
        next_bundle_hash: old.next_bundle_hash,
        phase: old.phase,
        relaunch: old.relaunch,
        recovery_relaunch_attempted: old.recovery_relaunch_attempted,
        error: old.error,
    })
}
