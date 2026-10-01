use super::storage::{document_dir, header_at, open_lock, require_directory, sync_directory};
use super::transaction::write_header;
use super::{field, format, now, Header};
use serde_json::Value;
use std::collections::HashSet;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::Instant;

static LAST_PRUNE: Mutex<Option<Instant>> = Mutex::new(None);

pub(super) fn inventory(root: &Path) -> Result<Vec<(PathBuf, Header)>, String> {
    require_directory(&root.join(&format().directory))?;
    let entries = match fs::read_dir(root.join(&format().directory)) {
        Ok(entries) => entries,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(e) => return Err(e.to_string()),
    };
    let mut documents = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|e| e.to_string())?;
        let kind = match entry.file_type() {
            Ok(kind) => kind,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
            Err(error) => return Err(error.to_string()),
        };
        if !kind.is_dir() {
            return Err("Invalid editor outbox directory.".into());
        }
        if let Some(header) = header_at(&entry.path())? {
            documents.push((entry.path(), header));
        }
    }
    Ok(documents)
}

pub(super) fn inventory_entry(header: Header) -> Value {
    let bytes = header.live_bytes();
    let mut entry = header.address;
    entry["bytes"] = bytes.into();
    entry["synchronized"] = header.synchronized.into();
    entry["retainedClients"] = serde_json::json!(header.retained_clients);
    entry
}

pub(super) fn list_at(root: &Path, project_id: &str) -> Result<Vec<Value>, String> {
    inventory(root)?
        .into_iter()
        .filter_map(|(_, header)| match field(&header.address, "projectId") {
            Ok(id) if project_id.is_empty() || id == project_id => {
                Some(Ok(inventory_entry(header)))
            }
            Ok(_) => None,
            Err(e) => Some(Err(e)),
        })
        .collect()
}

/// One document's inventory entry, read from its own header without scanning the directory.
pub(super) fn inspect_at(root: &Path, id: &str) -> Result<Option<Value>, String> {
    let directory = document_dir(root, id)?;
    let Some(header) = header_at(&directory)? else {
        return Ok(None);
    };
    if field(&header.address, "documentId")? != id {
        return Err("Invalid preserved editor header.".into());
    }
    Ok(Some(inventory_entry(header)))
}

pub(super) fn prune_at(
    root: &Path,
    keep: &str,
    now: u64,
    max_bytes: u64,
    max_documents: usize,
    max_age: u64,
) -> Result<(), String> {
    let mut documents = inventory(root)?;
    documents.retain(|(_, h)| h.synchronized && h.retained_clients.is_empty());
    documents
        .sort_by_key(|(_, h)| std::cmp::Reverse((h.address["documentId"] == keep, h.updated_at)));
    let mut bytes = 0;
    for (index, (path, header)) in documents.into_iter().enumerate() {
        bytes += header.live_bytes();
        if header.address["documentId"] != keep
            && (bytes > max_bytes
                || index >= max_documents
                || now.saturating_sub(header.updated_at) > max_age)
        {
            fs::remove_dir_all(path).map_err(|e| e.to_string())?;
        }
    }
    sync_directory(&root.join(&format().directory))
}

pub(super) fn sweep_orphaned_retention(
    root: &Path,
    live_clients: &HashSet<String>,
) -> Result<(), String> {
    for (directory, mut header) in inventory(root)? {
        let before = header.retained_clients.len();
        header
            .retained_clients
            .retain(|client| !client.starts_with("window:") || live_clients.contains(client));
        if before != header.retained_clients.len() {
            write_header(&directory, &header)?;
        }
    }
    Ok(())
}

pub(super) fn maintain(root: &Path, keep: &str) {
    let Ok(mut last) = LAST_PRUNE.lock() else {
        return;
    };
    if last.is_some_and(|time| time.elapsed().as_secs() < 60) {
        return;
    }
    let Ok(file) = open_lock(&root.join(&format().lock)) else {
        return;
    };
    if file.try_lock().is_err() {
        return;
    }
    *last = Some(Instant::now());
    // Cleanup failure leaves the durable commit valid.
    let _ = prune_at(
        root,
        keep,
        now(),
        format().cache_bytes,
        format().cache_documents,
        format().cache_max_age_seconds,
    );
    let locks = root.join(&format().lock).with_extension("d");
    if let Ok(entries) = fs::read_dir(locks) {
        for entry in entries.flatten() {
            if !root
                .join(&format().directory)
                .join(entry.file_name())
                .exists()
            {
                let _ = fs::remove_file(entry.path());
            }
        }
    }
}
