//! The activation journal is recovery-critical: it is refused unchanged, never quarantined.
use super::super::persistence;
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
    pub(super) next_bundle_hash: String,
    pub(super) phase: Phase,
    pub(super) relaunch: bool,
    pub(super) recovery_relaunch_attempted: bool,
    pub(super) error: Option<String>,
}
const RETAINED_RECEIPTS: usize = 20;
pub(super) fn active_path() -> Result<PathBuf, UpdateError> {
    Ok(persistence::update_dir()?.join("transaction.json"))
}
pub(super) fn read() -> Result<Option<Transaction>, UpdateError> {
    read_at(&active_path()?)
}
fn read_at(path: &std::path::Path) -> Result<Option<Transaction>, UpdateError> {
    persistence::read_json(path, Failure::JournalUnavailable, |t: &Transaction| {
        t.format_version == 1
            && uuid::Uuid::parse_str(&t.id).is_ok()
            && t.candidate.valid_identity()
            && t.target.is_absolute()
            && t.target.extension().and_then(|s| s.to_str()) == Some("app")
            && [&t.previous_hash, &t.next_hash, &t.next_bundle_hash]
                .iter()
                .all(|hash| persistence::hex_digest(hash))
    })
}
pub(super) fn write(t: &Transaction) -> Result<(), UpdateError> {
    persistence::write_json_atomic(
        &persistence::update_dir()?,
        "transaction.json",
        "transaction.tmp",
        t,
        Failure::JournalUnavailable,
    )
}
/// Archives a receipt and removes the active journal.
pub(super) fn retire(t: &Transaction) -> Result<(), UpdateError> {
    archive_receipt(t)?;
    persistence::remove_if_present(&active_path()?, Failure::JournalUnavailable)
}
pub(super) fn archive_receipt(transaction: &Transaction) -> Result<(), UpdateError> {
    let directory = persistence::update_dir()?.join("receipts");
    persistence::write_json_atomic(
        &directory,
        &format!("{}.json", transaction.id),
        &format!("{}.tmp", transaction.id),
        transaction,
        Failure::JournalUnavailable,
    )?;
    prune_receipts(&directory)
}
fn prune_receipts(directory: &std::path::Path) -> Result<(), UpdateError> {
    let mut receipts: Vec<_> = fs::read_dir(directory)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?
        .filter_map(Result::ok)
        .filter(|entry| {
            let path = entry.path();
            path.extension().and_then(|s| s.to_str()) == Some("json")
                && path
                    .file_stem()
                    .and_then(|s| s.to_str())
                    .is_some_and(|s| uuid::Uuid::parse_str(s).is_ok())
        })
        .map(|entry| {
            let modified = entry.metadata().and_then(|m| m.modified()).ok();
            (modified, entry.file_name(), entry.path())
        })
        .collect();
    receipts.sort();
    let excess = receipts.len().saturating_sub(RETAINED_RECEIPTS);
    for (_, _, path) in receipts.into_iter().take(excess) {
        fs::remove_file(path).map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn unknown_journal_shape_is_refused_without_modification() {
        let root = crate::test_support::TempDir::new("update-journal-future");
        let path = root.join("transaction.json");
        let bytes = br#"{"format_version":99,"future_transaction":"retained"}"#;
        fs::write(&path, bytes).unwrap();
        assert!(read_at(&path).is_err());
        assert_eq!(fs::read(path).unwrap(), bytes);
    }
    #[test]
    fn receipt_pruning_keeps_the_newest_receipts_and_ignores_temporary_files() {
        let root = crate::test_support::TempDir::new("update-receipts");
        let old = uuid::Uuid::new_v4();
        fs::write(root.join(format!("{old}.json")), b"{}").unwrap();
        let stale_temp = root.join(format!("{}.tmp-{}", uuid::Uuid::new_v4(), uuid::Uuid::new_v4()));
        fs::write(&stale_temp, b"").unwrap();
        let late = std::time::SystemTime::now() + std::time::Duration::from_secs(60);
        for _ in 0..RETAINED_RECEIPTS {
            let path = root.join(format!("{}.json", uuid::Uuid::new_v4()));
            fs::write(&path, b"{}").unwrap();
            fs::File::open(&path).unwrap().set_modified(late).unwrap();
        }
        prune_receipts(&root).unwrap();
        assert!(!root.join(format!("{old}.json")).exists());
        assert!(stale_temp.exists());
        let kept = fs::read_dir(&*root)
            .unwrap()
            .filter_map(Result::ok)
            .filter(|entry| entry.path().extension().and_then(|s| s.to_str()) == Some("json"))
            .count();
        assert_eq!(kept, RETAINED_RECEIPTS);
    }
}
