use super::*;
use super::{retention::*, storage::*, transaction::*};
use crate::test_support::TempDir;
use std::{
    collections::HashSet,
    fs,
    io::Write,
    path::{Path, PathBuf},
};
fn record(kind: &str) -> Value {
    serde_json::json!({"kind": kind, "documentId": "document", "clientId": "window", "operationId": "operation", "text": "draft", "projectId": "project", "rootId": "root", "fileId": "file", "path": "a.txt"})
}
fn directory(root: &Path) -> PathBuf {
    document_dir(root, "document").expect("document directory")
}
fn header(root: &Path) -> Header {
    header_at(&directory(root))
        .expect("read header")
        .expect("published header")
}

#[test]
fn a_document_writer_does_not_block_other_documents_or_committed_reads() {
    let root = TempDir::new("outbox-independent-documents");
    commit_at(&root, &[record("checkpoint")], &[]).expect("seed committed document");
    let store = store_lock(&root).expect("hold shared store lease");
    let document = document_lock(&root, "document").expect("hold document writer");
    let path = root.to_path_buf();
    let (send, receive) = std::sync::mpsc::channel();
    let worker = std::thread::spawn(move || {
        let _store = store_lock(&path).expect("join store during another write");
        let _document = document_lock(&path, "another").expect("acquire independent writer");
        let mut other = record("checkpoint");
        other["documentId"] = "another".into();
        commit_at(&path, &[other], &[]).expect("commit independent document");
        assert_eq!(
            read_at(&path, "document")
                .expect("read prior committed generation")
                .len(),
            1
        );
        assert_eq!(
            list_at(&path, "project")
                .expect("read inventory during write")
                .len(),
            2
        );
        send.send(()).expect("report independent completion");
    });
    let completed = receive.recv_timeout(std::time::Duration::from_secs(5));
    drop(document);
    drop(store);
    worker.join().expect("independent worker");
    completed.expect("another document must finish while the first writer remains held");
}

#[test]
fn concurrent_windows_preserve_every_record_in_one_document() {
    let root = TempDir::new("outbox-concurrent-windows");
    std::thread::scope(|scope| {
        for index in 0..8 {
            let root = &root;
            scope.spawn(move || {
                let _store = store_lock(root).expect("acquire store lease");
                let _document =
                    document_lock(root, "document").expect("serialize document writers");
                let mut checkpoint = record("checkpoint");
                checkpoint["clientId"] = format!("window-{index}").into();
                commit_at(root, &[checkpoint], &[]).expect("preserve window history");
            });
        }
    });
    assert_eq!(
        read_at(&root, "document").expect("read all windows").len(),
        8
    );
}

#[test]
fn capture_and_restore_exclude_shared_document_activity() {
    let root = TempDir::new("outbox-capture-lease");
    let activity = store_lock(&root).expect("document activity");
    let capture = open_lock(&root.join(&format().lock)).expect("capture lock file");
    assert!(
        capture.try_lock().is_err(),
        "capture must wait for document activity"
    );
    drop(activity);
    capture
        .try_lock()
        .expect("capture starts after activity releases");
    let reader = open_lock(&root.join(&format().lock)).expect("reader lock file");
    assert!(
        reader.try_lock_shared().is_err(),
        "document activity must wait for capture"
    );
}

#[test]
fn commits_append_without_rewriting_earlier_records() {
    let root = TempDir::new("outbox-append-only");
    let mut checkpoint = record("checkpoint");
    checkpoint["text"] = "x".repeat(50_000).into();
    commit_at(&root, &[checkpoint.clone()], &[]).expect("seed checkpoint");
    let first = header(&root);
    let log = directory(&root).join(&first.log);
    let before = fs::read(&log).expect("read first generation");
    assert_eq!(before.len() as u64, first.log_bytes);
    commit_at(&root, &[record("update")], &[]).expect("append an update");
    let second = header(&root);
    let after = fs::read(&log).expect("read appended generation");
    assert_eq!(
        second.log, first.log,
        "small commits stay in the same generation"
    );
    assert_eq!(
        &after[..before.len()],
        &before[..],
        "earlier frames are never rewritten"
    );
    assert_eq!(second.frames.len(), 2);
    assert_eq!(second.frames[1].offset, first.log_bytes);
    assert_eq!(after.len() as u64, second.log_bytes);
    let records = read_at(&root, "document").expect("read both records");
    assert_eq!(records[0], checkpoint);
    assert_eq!(records[1], record("update"));
}

#[test]
fn an_unpublished_tail_is_ignored_and_dropped_by_the_next_commit() {
    let root = TempDir::new("outbox-torn-tail");
    commit_at(&root, &[record("checkpoint")], &[]).expect("seed checkpoint");
    let published = header(&root);
    let log = directory(&root).join(&published.log);
    let mut file = fs::OpenOptions::new()
        .append(true)
        .open(&log)
        .expect("open log");
    file.write_all(b"{\"kind\":\"update\",\"documentId\":\"document\",\"clientId\":\"win")
        .expect("interrupted append");
    drop(file);
    assert_eq!(
        read_at(&root, "document")
            .expect("read past the torn tail")
            .len(),
        1
    );
    commit_at(&root, &[record("format")], &[]).expect("commit after an interruption");
    let next = header(&root);
    assert_eq!(fs::metadata(&log).expect("log size").len(), next.log_bytes);
    assert_eq!(
        read_at(&root, "document")
            .expect("read committed records")
            .len(),
        2
    );
}

#[test]
fn compaction_rotates_generations_and_keeps_live_records() {
    let root = TempDir::new("outbox-compaction");
    let mut first = record("checkpoint");
    first["text"] = "a".repeat(10_000).into();
    commit_with(&root, &[first], &[], 1).expect("seed generation one");
    let pending = record("update");
    commit_with(&root, &[pending.clone()], &[], 1).expect("append pending work");
    let mut replacement = record("checkpoint");
    replacement["text"] = "b".repeat(100).into();
    commit_with(&root, &[replacement.clone()], &[], 1).expect("replace the checkpoint");
    let compacted = header(&root);
    assert_eq!(compacted.log, log_name(2));
    assert!(
        !directory(&root).join(log_name(1)).exists(),
        "the retired generation is removed"
    );
    assert_eq!(compacted.log_bytes, compacted.live_bytes());
    let records = read_at(&root, "document").expect("read compacted generation");
    assert_eq!(records.len(), 2);
    assert!(records.contains(&pending) && records.contains(&replacement));
    commit_with(&root, &[record("command")], &[], 1).expect("append to the new generation");
    assert_eq!(
        read_at(&root, "document")
            .expect("read after rotation")
            .len(),
        3
    );
}

#[test]
fn readers_validate_one_generation_while_a_document_is_replaced() {
    let root = TempDir::new("outbox-atomic-generation");
    commit_at(&root, &[record("checkpoint")], &[]).expect("seed committed generation");
    std::thread::scope(|scope| {
        scope.spawn(|| {
            for size in [10, 100_000].into_iter().cycle().take(32) {
                let _store = store_lock(&root).expect("writer store lease");
                let _document = document_lock(&root, "document").expect("writer document lease");
                let mut checkpoint = record("checkpoint");
                checkpoint["text"] = "x".repeat(size).into();
                commit_with(&root, &[checkpoint], &[], 1).expect("replace generation");
            }
        });
        for _ in 0..128 {
            let _store = store_lock(&root).expect("reader store lease");
            let records = read_at(&root, "document").expect("read a complete generation");
            assert_eq!(records.len(), 1);
            assert!(matches!(
                records[0]["text"].as_str().expect("document text").len(),
                5 | 10 | 100_000
            ));
        }
    });
}

#[test]
fn inventory_reports_synchronized_checkpoints_and_pending_work() {
    let root = TempDir::new("document-outbox-inventory");
    let mut checkpoint = record("checkpoint");
    checkpoint["synchronized"] = true.into();
    commit_at(&root, &[checkpoint], &[]).expect("write synchronized history");
    assert_eq!(
        list_at(&root, "project").expect("list synchronized")[0]["synchronized"],
        true
    );
    let pending = record("update");
    commit_at(&root, std::slice::from_ref(&pending), &[]).expect("write pending edit");
    assert_eq!(
        list_at(&root, "project").expect("list pending")[0]["synchronized"],
        false
    );
    commit_at(&root, &[], &[pending]).expect("remove accepted edit");
    assert_eq!(
        list_at(&root, "project").expect("list settled")[0]["synchronized"],
        true
    );
}

#[test]
fn record_kinds_with_the_same_operation_survive_independently() {
    let root = TempDir::new("document-outbox");
    let command = record("command");
    let checkpoint = record("checkpoint");
    let pending = record("update");
    let format = record("format");
    commit_at(&root, std::slice::from_ref(&checkpoint), &[]).expect("write checkpoint");
    commit_at(&root, std::slice::from_ref(&command), &[]).expect("write command");
    commit_at(&root, std::slice::from_ref(&pending), &[]).expect("write update");
    commit_at(&root, std::slice::from_ref(&format), &[]).expect("write format intent");
    assert_eq!(read_at(&root, "document").expect("read records").len(), 4);
    assert_eq!(
        list_at(&root, "project")
            .expect("discover preserved file")
            .len(),
        1
    );
    assert!(list_at(&root, "another-project")
        .expect("isolate project")
        .is_empty());
    commit_at(&root, &[], std::slice::from_ref(&command)).expect("acknowledge command");
    let records = read_at(&root, "document").expect("read remaining work");
    assert_eq!(records.len(), 3);
    assert!(records.contains(&pending));
    assert!(records.contains(&format));
    assert!(records.contains(&checkpoint));
}

#[test]
fn corrupt_preserved_work_is_refused_without_removing_it() {
    let root = TempDir::new("document-outbox-corrupt");
    let pending = record("update");
    commit_at(&root, &[record("checkpoint"), pending], &[]).expect("write pending update");
    let log = directory(&root).join(&header(&root).log);
    fs::write(&log, "interrupted").expect("inject corrupt records");
    assert!(read_at(&root, "document").is_err());
    assert!(commit_at(&root, &[record("format")], &[]).is_err());
    assert_eq!(
        fs::read_to_string(log).expect("retained bytes"),
        "interrupted"
    );
}

#[test]
fn history_and_updates_commit_together_without_overwriting_another_window() {
    let root = TempDir::new("document-history-transaction");
    let checkpoint = record("checkpoint");
    let pending = record("update");
    let mut peer = record("checkpoint");
    peer["clientId"] = "peer".into();
    commit_at(&root, std::slice::from_ref(&peer), &[]).expect("preserve peer history");
    commit_at(&root, &[checkpoint.clone(), pending.clone()], &[])
        .expect("preserve draft and history");
    let saved = read_at(&root, "document").expect("read complete transaction");
    assert!(saved.contains(&checkpoint) && saved.contains(&pending) && saved.contains(&peer));
    let mut invalid = record("update");
    invalid["documentId"] = "another-document".into();
    assert!(commit_at(&root, &[record("command"), invalid], &[]).is_err());
    assert_eq!(
        read_at(&root, "document").expect("read rejected transaction"),
        saved
    );
}

#[test]
fn automatic_retention_preserves_pending_work_and_reclaims_only_acknowledged_history() {
    let root = TempDir::new("outbox-retention");
    for id in ["old", "recent", "pending", "command", "format"] {
        let mut checkpoint = record("checkpoint");
        checkpoint["documentId"] = id.into();
        checkpoint["synchronized"] = true.into();
        let mut records = vec![checkpoint];
        if matches!(id, "pending" | "command" | "format") {
            let mut pending = record(if id == "pending" { "update" } else { id });
            pending["documentId"] = id.into();
            records.push(pending);
        }
        commit_at(&root, &records, &[]).expect("seed document");
    }
    prune_at(&root, "recent", now() + 100, 1, 1, 1).expect("automatic cleanup");
    assert!(read_at(&root, "old")
        .expect("evicted checkpoint")
        .is_empty());
    for id in ["recent", "pending", "command", "format"] {
        assert!(!read_at(&root, id).expect("protected document").is_empty());
    }
}

#[test]
fn another_window_acknowledges_a_closed_windows_checkpoint_without_touching_its_bytes() {
    let root = TempDir::new("outbox-cross-window-ack");
    let mut checkpoint = record("checkpoint");
    checkpoint["pendingOperations"] = serde_json::json!(["operation"]);
    checkpoint["synchronized"] = false.into();
    checkpoint["history"] = serde_json::json!({"done": ["retained undo"]});
    let mut pending = record("update");
    pending["acknowledged"] = false.into();
    commit_at(&root, &[checkpoint.clone(), pending.clone()], &[]).expect("preserve pending work");
    let stored = header(&root).frames[0].clone();
    pending["acknowledged"] = true.into();
    commit_at(&root, &[pending.clone()], &[]).expect("another window delivered the update");
    commit_at(&root, &[], &[pending]).expect("compact acknowledged transport record");
    let settled = header(&root);
    assert_eq!(settled.frames[0].offset, stored.offset);
    assert_eq!(
        settled.frames[0].sha256, stored.sha256,
        "acknowledgement leaves the checkpoint frame in place"
    );
    let saved = read_at(&root, "document").expect("read checkpoint");
    assert_eq!(saved[0]["synchronized"], true);
    assert_eq!(saved[0]["pendingOperations"], serde_json::json!([]));
    assert_eq!(
        saved[0]["history"],
        serde_json::json!({"done": ["retained undo"]})
    );
    prune_at(&root, "", now() + 100, 1, 0, 1).expect("reclaim delivered closed document");
    assert!(read_at(&root, "document")
        .expect("checkpoint reclaimed")
        .is_empty());
}

#[test]
fn future_formats_remain_untouched() {
    let root = TempDir::new("outbox-future-format");
    commit_at(&root, &[record("checkpoint")], &[]).expect("seed envelope");
    let path = directory(&root).join(&format().header);
    let original =
        fs::read_to_string(&path)
            .expect("read header")
            .replacen("\"format\":1", "\"format\":2", 1);
    fs::write(&path, &original).expect("write future format");
    assert!(read_at(&root, "document").is_err());
    assert!(commit_at(&root, &[record("checkpoint")], &[]).is_err());
    assert_eq!(
        fs::read_to_string(path).expect("read untouched bytes"),
        original
    );
}

#[test]
fn a_live_document_can_save_after_its_acknowledged_cache_was_reclaimed() {
    let root = TempDir::new("outbox-reclaimed-active-document");
    let mut checkpoint = record("checkpoint");
    checkpoint["synchronized"] = true.into();
    commit_at(&root, &[checkpoint], &[]).expect("preserve synchronized checkpoint");
    prune_at(&root, "", now() + 100, 1, 0, 1).expect("reclaim checkpoint cache");
    commit_at(&root, &[record("command")], &[])
        .expect("preserve later save intent without a checkpoint");
    assert_eq!(
        list_at(&root, "project")
            .expect("discover save intent")
            .len(),
        1
    );
    assert_eq!(
        read_at(&root, "document").expect("read durable save")[0]["kind"],
        "command"
    );
}

#[cfg(unix)]
#[test]
fn restored_temporary_symlinks_cannot_redirect_preservation() {
    let root = TempDir::new("outbox-symlink");
    commit_at(&root, &[record("checkpoint")], &[]).expect("seed envelope");
    let outside = root.join("outside");
    fs::write(&outside, "original").expect("write outside file");
    let temporary = directory(&root).join(format!("{}.tmp", format().header));
    std::os::unix::fs::symlink(&outside, &temporary).expect("inject archive link");
    assert!(commit_at(&root, &[record("command")], &[]).is_err());
    assert_eq!(
        fs::read_to_string(&outside).expect("read outside file"),
        "original"
    );
    assert!(fs::symlink_metadata(&temporary)
        .expect("preserve refused entry")
        .file_type()
        .is_symlink());
}
#[test]
fn open_undo_history_survives_cache_pressure_and_close_releases_it() {
    let root = TempDir::new("outbox-open-history");
    let mut checkpoint = record("checkpoint");
    checkpoint["synchronized"] = true.into();
    let mut retention = Retention {
        document: checkpoint.clone(),
        client_id: "window".into(),
        retained: true,
    };
    commit_retaining(&root, &[checkpoint.clone()], &[], Some(&retention))
        .expect("commit retained history");
    prune_at(&root, "", now() + 100, 1, 0, 1).expect("apply pressure");
    assert_eq!(
        read_at(&root, "document").expect("read open history"),
        vec![checkpoint]
    );
    retention.retained = false;
    commit_retaining(&root, &[], &[], Some(&retention)).expect("close tab");
    prune_at(&root, "", now() + 100, 1, 0, 1).expect("reclaim closed history");
    assert!(read_at(&root, "document")
        .expect("read closed cache")
        .is_empty());
}

#[test]
fn inspect_reads_one_header_without_the_inventory() {
    let root = TempDir::new("outbox-inspect");
    assert!(inspect_at(&root, "document")
        .expect("inspect empty store")
        .is_none());
    let checkpoint = record("checkpoint");
    let retention = Retention {
        document: checkpoint.clone(),
        client_id: "window".into(),
        retained: true,
    };
    commit_retaining(&root, &[checkpoint], &[], Some(&retention)).expect("retain tab");
    let mut other = record("checkpoint");
    other["documentId"] = "another".into();
    commit_at(&root, &[other], &[]).expect("seed unrelated document");
    let entry = inspect_at(&root, "document")
        .expect("inspect retained document")
        .expect("published entry");
    assert_eq!(entry["documentId"], "document");
    assert_eq!(entry["retainedClients"], serde_json::json!(["window"]));
    assert_eq!(
        entry["bytes"],
        serde_json::json!(header(&root).live_bytes())
    );
    assert_eq!(entry["synchronized"], serde_json::json!(false));
    assert!(inspect_at(&root, "missing")
        .expect("inspect absent document")
        .is_none());
}

#[test]
fn retention_requires_a_covering_checkpoint() {
    let root = TempDir::new("outbox-uncovered-retention");
    let pending = record("update");
    let retention = Retention {
        document: pending.clone(),
        client_id: "window".into(),
        retained: true,
    };
    assert!(commit_retaining(&root, &[pending], &[], Some(&retention)).is_err());
    assert!(read_at(&root, "document")
        .expect("read refused transaction")
        .is_empty());
}

#[test]
fn bounded_read_rejects_growth_before_loading_frames() {
    let root = TempDir::new("outbox-bounded-read");
    commit_at(&root, &[record("checkpoint")], &[]).expect("write recovery");
    let size = header(&root).live_bytes();
    assert!(read_bounded(&root, "document", size - 1).is_err());
    assert_eq!(
        read_bounded(&root, "document", size)
            .expect("read admitted recovery")
            .len(),
        1
    );
    assert_eq!(header(&root).live_bytes(), size);
}

#[test]
fn orphan_sweep_releases_windows_absent_from_native_inventory() {
    let root = TempDir::new("outbox-orphan-windows");
    for client in ["window:live", "window:restored", "window:lost"] {
        let mut checkpoint = record("checkpoint");
        checkpoint["clientId"] = client.into();
        let retention = Retention {
            document: checkpoint.clone(),
            client_id: client.into(),
            retained: true,
        };
        commit_retaining(&root, &[checkpoint], &[], Some(&retention))
            .expect("retain window history");
    }
    crate::write_app_state_slice(
        &root,
        "filesHotExit",
        &serde_json::json!({"byProject": {"project": {"buffers": [
            {"documentId": "document", "clientId": "window:restored"}
        ]}}}),
    )
    .expect("persist restoration inventory");
    sweep_orphaned_retention(&root, &HashSet::from(["window:live".to_string()]))
        .expect("sweep abandoned references");
    assert_eq!(header(&root).retained_clients, vec!["window:live"]);
    assert_eq!(
        read_at(&root, "document")
            .expect("pending records remain")
            .len(),
        3
    );
}
