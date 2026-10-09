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
    pub(super) prepared_bundle: PathBuf,
    pub(super) previous_hash: String,
    pub(super) next_hash: String,
    pub(super) next_bundle_hash: String,
    pub(super) phase: Phase,
    pub(super) relaunch: bool,
    pub(super) recovery_relaunch_attempted: bool,
    pub(super) error: Option<String>,
}
mod legacy;
const RETAINED_RECEIPTS: usize = 20;
pub(super) fn active_path() -> Result<PathBuf, UpdateError> {
    Ok(persistence::update_dir()?.join("transaction.json"))
}
pub(super) fn read() -> Result<Option<Transaction>, UpdateError> {
    let _lock = super::super::record_lock::acquire(&persistence::update_dir()?, "records.lock")?;
    read_at(&active_path()?)
}
fn read_at(path: &std::path::Path) -> Result<Option<Transaction>, UpdateError> {
    let value: Option<serde_json::Value> =
        persistence::read_json(path, Failure::JournalUnavailable, |_| true)?;
    if let Some(original) = value.filter(|v| v["format_version"] == 1) {
        let converted = legacy::decode(original.clone())?;
        if !valid(&converted) {
            return Err(Failure::JournalUnavailable.into());
        }
        let dir = path.parent().ok_or(Failure::JournalUnavailable)?;
        persistence::write_json_atomic(
            &dir.join("receipts"),
            &format!("{}-format-1.json", converted.id),
            "migration.tmp",
            &original,
            Failure::JournalUnavailable,
        )?;
        persistence::write_json_atomic(
            dir,
            "transaction.json",
            "transaction.tmp",
            &converted,
            Failure::JournalUnavailable,
        )?;
        prune_receipts(&dir.join("receipts"))?;
    }
    persistence::read_json(path, Failure::JournalUnavailable, valid)
}
fn valid(t: &Transaction) -> bool {
    t.format_version == 2
        && uuid::Uuid::parse_str(&t.id).is_ok()
        && t.candidate.valid_identity()
        && t.target.is_absolute()
        && t.target.extension().and_then(|s| s.to_str()) == Some("app")
        && super::installer::valid_prepared_path(&t.target, &t.candidate, &t.prepared_bundle)
        && [&t.previous_hash, &t.next_hash, &t.next_bundle_hash]
            .iter()
            .all(|h| persistence::hex_digest(h))
}
fn transition_allowed(current: &Transaction, next: &Transaction) -> bool {
    if current.id != next.id {
        return next.phase == Phase::Prepared
            && match current.phase {
                Phase::Committed => false,
                Phase::Activated => {
                    current.target == next.target && current.next_hash == next.previous_hash
                }
                _ => true,
            };
    }
    if current.target != next.target
        || current.prepared_bundle != next.prepared_bundle
        || current.previous_hash != next.previous_hash
        || current.next_hash != next.next_hash
        || current.next_bundle_hash != next.next_bundle_hash
        || current.candidate.release_id != next.candidate.release_id
    {
        return false;
    }
    matches!(
        (current.phase, next.phase),
        (Phase::Prepared, Phase::Prepared | Phase::Committed)
            | (
                Phase::Committed,
                Phase::Committed | Phase::Activated | Phase::Failed
            )
            | (Phase::Activated, Phase::Activated | Phase::StartupConfirmed)
            | (Phase::StartupConfirmed, Phase::StartupConfirmed)
            | (Phase::Failed, Phase::Failed)
    )
}
pub(super) fn write(t: &Transaction) -> Result<(), UpdateError> {
    let _lock = super::super::record_lock::acquire(&persistence::update_dir()?, "records.lock")?;
    if !valid(t) {
        return Err(Failure::JournalUnavailable.into());
    }
    if let Some(current) = read_at(&active_path()?)? {
        if current.phase == Phase::Prepared && t.phase == Phase::Committed {
            let ready = super::staging::read_ready_unlocked()?.ok_or(Failure::CandidateMissing)?;
            if ready.candidate.release_id != t.candidate.release_id {
                return Err(Failure::CandidateChanged.into());
            }
            if !super::offer_is_fresh(ready.offer_confirmed_at, super::super::now()) {
                return Err(Failure::FeedRejected.into());
            }
        }
        if !transition_allowed(&current, t) {
            return Err(Failure::InvalidTransition.into());
        }
    } else if t.phase != Phase::Prepared {
        return Err(Failure::InvalidTransition.into());
    }
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
    let _lock = super::super::record_lock::acquire(&persistence::update_dir()?, "records.lock")?;
    if read_at(&active_path()?)?
        .is_some_and(|current| current.id != t.id || current.phase != t.phase)
    {
        return Err(Failure::InvalidTransition.into());
    }
    archive_receipt_unlocked(t)?;
    persistence::remove_if_present(&active_path()?, Failure::JournalUnavailable)
}
pub(super) fn archive_receipt(transaction: &Transaction) -> Result<(), UpdateError> {
    let _lock = super::super::record_lock::acquire(&persistence::update_dir()?, "records.lock")?;
    archive_receipt_unlocked(transaction)
}
fn archive_receipt_unlocked(transaction: &Transaction) -> Result<(), UpdateError> {
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
                && path.file_stem().and_then(|s| s.to_str()).is_some_and(|s| {
                    uuid::Uuid::parse_str(s.strip_suffix("-format-1").unwrap_or(s)).is_ok()
                })
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
    #[cfg(unix)]
    #[test]
    fn released_journal_migration_retains_original_record_and_legacy_path() {
        let root = crate::test_support::TempDir::new("update-journal-migrate");
        let bytes = include_bytes!("../fixtures/transaction-v1.json");
        let old: serde_json::Value = serde_json::from_slice(bytes).unwrap();
        let path = root.join("transaction.json");
        fs::write(&path, bytes).unwrap();
        let migrated = read_at(&path).unwrap().unwrap();
        assert_eq!(migrated.format_version, 2);
        assert_eq!(migrated.phase, Phase::Committed);
        assert!(migrated.relaunch);
        assert!(!migrated.recovery_relaunch_attempted);
        assert_eq!(
            migrated.target,
            PathBuf::from("/Applications/Painted Wolf Code.app")
        );
        assert_eq!(
            migrated.prepared_bundle,
            super::super::installer::legacy_prepared_path(&migrated.target, &migrated.candidate)
                .unwrap()
        );
        let retained: serde_json::Value = serde_json::from_slice(
            &fs::read(
                root.join("receipts")
                    .join(format!("{}-format-1.json", migrated.id)),
            )
            .unwrap(),
        )
        .unwrap();
        assert_eq!(retained, old);
        assert_eq!(
            read_at(&path).unwrap().unwrap().prepared_bundle,
            migrated.prepared_bundle
        );
    }
    #[cfg(unix)]
    #[test]
    fn journal_transitions_refuse_stale_writers_and_allow_forward_recovery() {
        let root = crate::test_support::TempDir::new("update-journal-transitions");
        let mut current = super::super::tests::fixture(&root);
        let mut next = current.clone();
        current.phase = Phase::StartupConfirmed;
        next.phase = Phase::Activated;
        assert!(!transition_allowed(&current, &next));
        current.phase = Phase::Prepared;
        next.phase = Phase::Committed;
        assert!(transition_allowed(&current, &next));
        next.id = uuid::Uuid::new_v4().to_string();
        assert!(!transition_allowed(&current, &next));
        current.phase = Phase::Activated;
        next.phase = Phase::Prepared;
        assert!(!transition_allowed(&current, &next));
        next.previous_hash = current.next_hash.clone();
        assert!(transition_allowed(&current, &next));
    }
    #[test]
    fn unknown_legacy_fields_preserve_the_active_record() {
        let root = crate::test_support::TempDir::new("update-journal-unknown-legacy");
        let path = root.join("transaction.json");
        let mut value: serde_json::Value =
            serde_json::from_str(include_str!("../fixtures/transaction-v1.json")).unwrap();
        value["unknown_intent"] = true.into();
        let bytes = serde_json::to_vec(&value).unwrap();
        fs::write(&path, &bytes).unwrap();
        assert!(read_at(&path).is_err());
        assert_eq!(fs::read(path).unwrap(), bytes);
        assert!(!root.join("receipts").exists());
    }
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
        let stale_temp = root.join(format!(
            "{}.tmp-{}",
            uuid::Uuid::new_v4(),
            uuid::Uuid::new_v4()
        ));
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
