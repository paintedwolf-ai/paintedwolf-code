//! Durable editor records and native commands. Header publication remains the transaction boundary.

mod retention;
mod storage;
#[cfg(test)]
mod tests;
mod transaction;

use self::retention::{inspect_at, list_at, maintain, sweep_orphaned_retention};
use self::storage::{document_lock, open_lock, read_bounded, store_lock};
use self::transaction::{commit_retaining, Retention};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::sync::OnceLock;
use std::time::{SystemTime, UNIX_EPOCH};
use tauri::Manager;

#[derive(Deserialize)]
struct Format {
    version: u32,
    directory: String,
    header: String,
    log_prefix: String,
    log_suffix: String,
    lock: String,
    max_document_bytes: u64,
    max_record_bytes: u64,
    max_header_bytes: u64,
    compaction_bytes: u64,
    cache_bytes: u64,
    cache_documents: usize,
    cache_max_age_seconds: u64,
}

fn format() -> &'static Format {
    static FORMAT: OnceLock<Format> = OnceLock::new();
    FORMAT.get_or_init(|| {
        serde_json::from_str(include_str!(
            "../../../lycaon/internal/editoroutbox/format.json"
        ))
        .expect("embedded editor outbox format")
    })
}

/// One preserved record inside a generation log.
#[derive(Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Frame {
    name: String,
    kind: String,
    offset: u64,
    length: u64,
    sha256: String,
    /// Checkpoint bookkeeping lives here so acknowledgements never rewrite the checkpoint bytes.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending: Option<Vec<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    synchronized: Option<bool>,
}

/// The commit point: replacing this file publishes one transaction.
#[derive(Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Header {
    format: u32,
    address: Value,
    updated_at: u64,
    synchronized: bool,
    log: String,
    log_bytes: u64,
    frames: Vec<Frame>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    retained_clients: Vec<String>,
}

impl Header {
    fn synchronized_by_frames(frames: &[Frame]) -> bool {
        frames
            .iter()
            .all(|frame| frame.kind == "checkpoint" && frame.synchronized == Some(true))
    }

    fn live_bytes(&self) -> u64 {
        self.frames.iter().map(|frame| frame.length).sum()
    }
}

fn digest(value: &[u8]) -> String {
    format!("{:x}", Sha256::digest(value))
}
fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

fn field<'a>(record: &'a Value, name: &str) -> Result<&'a str, String> {
    record
        .get(name)
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty() && value.len() <= 256)
        .ok_or_else(|| format!("Invalid document outbox {name}."))
}

fn record_kind(record: &Value) -> Result<&str, String> {
    let kind = field(record, "kind")?;
    if !matches!(kind, "checkpoint" | "update" | "command" | "format") {
        return Err("Unknown document outbox record.".into());
    }
    Ok(kind)
}

fn record_name(record: &Value) -> Result<String, String> {
    let kind = record_kind(record)?;
    let client = field(record, "clientId")?;
    let operation = if kind == "checkpoint" {
        "checkpoint"
    } else {
        field(record, "operationId")?
    };
    Ok(digest(format!("{client}\0{kind}\0{operation}").as_bytes()))
}

pub fn reconcile_windows(app: &tauri::AppHandle, destroyed: Option<&str>) {
    let Some(root) = crate::den_state_dir() else {
        return;
    };
    let app = app.clone();
    let destroyed = destroyed.map(str::to_owned);
    tauri::async_runtime::spawn_blocking(move || {
        let Ok(file) = open_lock(&root.join(&format().lock)) else {
            return;
        };
        if file.lock().is_err() {
            return;
        }
        let live_clients = app
            .webview_windows()
            .keys()
            .filter(|label| destroyed.as_deref() != Some(label.as_str()))
            .map(|label| format!("window:{label}"))
            .collect();
        // Read the inventory under the store lock so newer commits cannot precede it.
        let _ = sweep_orphaned_retention(&root, &live_clients);
    });
}

#[tauri::command]
pub async fn list_document_outbox(project_id: Option<String>) -> Result<Vec<Value>, String> {
    let root = crate::den_state_dir().ok_or("Editor storage is unavailable.")?;
    let read_root = root.clone();
    let result = tauri::async_runtime::spawn_blocking(move || {
        let _store = store_lock(&read_root)?;
        list_at(&read_root, project_id.as_deref().unwrap_or(""))
    })
    .await
    .map_err(|e| e.to_string())?;
    tauri::async_runtime::spawn_blocking(move || maintain(&root, ""));
    result
}

#[tauri::command]
pub async fn inspect_document_outbox(document_id: String) -> Result<Option<Value>, String> {
    let root = crate::den_state_dir().ok_or("Editor storage is unavailable.")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _store = store_lock(&root)?;
        inspect_at(&root, &document_id)
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn read_document_outbox(
    document_id: String,
    max_bytes: Option<u64>,
) -> Result<Vec<Value>, String> {
    let root = crate::den_state_dir().ok_or("Editor storage is unavailable.")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _store = store_lock(&root)?;
        read_bounded(
            &root,
            &document_id,
            max_bytes.unwrap_or(format().max_document_bytes),
        )
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn commit_document_outbox(
    records: Vec<Value>,
    remove: Vec<Value>,
    retention: Option<Retention>,
) -> Result<(), String> {
    let root = crate::den_state_dir().ok_or("Editor storage is unavailable.")?;
    let Some(first) = records
        .first()
        .or_else(|| remove.first())
        .or_else(|| retention.as_ref().map(|r| &r.document))
    else {
        return Ok(());
    };
    let id = field(first, "documentId")?.to_owned();
    let write_root = root.clone();
    let keep = id.clone();
    tauri::async_runtime::spawn_blocking(move || {
        let _store = store_lock(&write_root)?;
        let _document = document_lock(&write_root, &id)?;
        commit_retaining(&write_root, &records, &remove, retention.as_ref())
    })
    .await
    .map_err(|e| e.to_string())??;
    tauri::async_runtime::spawn_blocking(move || maintain(&root, &keep));
    Ok(())
}
