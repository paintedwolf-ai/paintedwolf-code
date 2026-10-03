use super::{Candidate, Failure, UpdateError};
use serde::{Deserialize, Serialize};
use std::{fs, path::PathBuf};
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub(super) enum Phase {
    Prepared,
    Committed,
    Activated,
    StartupConfirmed,
    Failed,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Transaction {
    pub(super) format_version: u8,
    pub(super) id: String,
    pub(super) candidate: Candidate,
    pub(super) target: PathBuf,
    pub(super) previous_hash: String,
    pub(super) next_hash: String,
    pub(super) phase: Phase,
    pub(super) relaunch: bool,
    pub(super) recovery_relaunch_attempted: bool,
    pub(super) error: Option<String>,
}
pub(super) fn active_path() -> Result<PathBuf, UpdateError> {
    Ok(super::super::persistence::update_dir()?.join("transaction.json"))
}
pub(super) fn read() -> Result<Option<Transaction>, UpdateError> {
    let raw = match fs::read(active_path()?) {
        Ok(raw) => raw,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(UpdateError::new(Failure::JournalUnavailable, e)),
    };
    let t: Transaction = serde_json::from_slice(&raw)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    if t.format_version != 1
        || uuid::Uuid::parse_str(&t.id).is_err()
        || !t.candidate.valid_identity()
        || !t.target.is_absolute()
        || t.target.extension().and_then(|s| s.to_str()) != Some("app")
    {
        return Err(Failure::JournalUnavailable.into());
    }
    Ok(Some(t))
}
pub(super) fn write(t: &Transaction) -> Result<(), UpdateError> {
    super::super::persistence::write_json_atomic(
        &super::super::persistence::update_dir()?,
        "transaction.json",
        "transaction.tmp",
        t,
        Failure::JournalUnavailable,
    )
}
pub(super) fn archive_receipt(transaction: &Transaction) -> Result<(), UpdateError> {
    let directory = super::super::persistence::update_dir()?.join("receipts");
    super::super::persistence::write_json_atomic(
        &directory,
        &format!("{}.json", transaction.id),
        &format!("{}.tmp", transaction.id),
        transaction,
        Failure::JournalUnavailable,
    )?;
    let mut entries: Vec<_> = fs::read_dir(&directory)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?
        .filter_map(Result::ok)
        .filter(|e| {
            e.path()
                .file_stem()
                .and_then(|s| s.to_str())
                .is_some_and(|s| uuid::Uuid::parse_str(s).is_ok())
        })
        .collect();
    entries.sort_by_key(|e| e.metadata().and_then(|m| m.modified()).ok());
    let excess = entries.len().saturating_sub(20);
    for entry in entries.into_iter().take(excess) {
        fs::remove_file(entry.path())
            .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    }
    Ok(())
}
