//! Durable Home submissions. One manifest is replaced atomically under an
//! interprocess lock; attachment bytes are published before the manifest marks
//! them ready, and unreferenced bytes are swept by reachability from it.

use crate::atomic_file;
use serde::Serialize;
use serde_json::Value;
use std::collections::HashSet;
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::time::UNIX_EPOCH;

const DIRECTORY: &str = "home-starts";
const MANIFEST: &str = "manifest.json";
const LOCK: &str = "home-starts.lock";
const BLOBS: &str = "blobs";
const ITEM_HEADER: &str = "x-home-item";
const MAX_MANIFEST_BYTES: u64 = 16 * 1024 * 1024;
const MAX_BLOB_BYTES: u64 = 512 * 1024 * 1024;

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CommitOutcome {
    committed: bool,
    manifest: Option<Value>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Fingerprint {
    size: u64,
    modified_ms: u64,
}

fn root() -> Result<PathBuf, String> {
    let base = crate::config_dir::host_config_dir().map_err(|_| "Home storage is unavailable.".to_string())?;
    Ok(base.join(DIRECTORY))
}

fn private_file(path: &Path) -> std::io::Result<fs::File> {
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)
}

// Every reader and writer, in every app process, holds this lock.
fn lock(root: &Path) -> Result<fs::File, String> {
    crate::config_dir::ensure_private_dir(root).map_err(|e| e.to_string())?;
    let mut options = fs::OpenOptions::new();
    options.read(true).write(true).create(true).truncate(false);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let file = options.open(root.join(LOCK)).map_err(|e| e.to_string())?;
    file.lock().map_err(|e| e.to_string())?;
    Ok(file)
}

fn valid_item_id(id: &str) -> bool {
    id.len() == 36 && id.chars().all(|c| c.is_ascii_hexdigit() || c == '-')
}

fn read_manifest(root: &Path) -> Result<Option<Value>, String> {
    let path = root.join(MANIFEST);
    let file = match fs::File::open(&path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error.to_string()),
    };
    let mut raw = Vec::new();
    file.take(MAX_MANIFEST_BYTES + 1)
        .read_to_end(&mut raw)
        .map_err(|e| e.to_string())?;
    if raw.len() as u64 > MAX_MANIFEST_BYTES {
        return Err("Saved Home ideas exceed their storage limit.".into());
    }
    let value: Value = serde_json::from_slice(&raw)
        .map_err(|_| "Saved Home ideas have an unknown format; they were left untouched.".to_string())?;
    if value.get("format").and_then(Value::as_u64) != Some(1) {
        return Err("Saved Home ideas have an unknown format; they were left untouched.".into());
    }
    Ok(Some(value))
}

fn revision(manifest: &Option<Value>) -> u64 {
    manifest
        .as_ref()
        .and_then(|value| value.get("revision"))
        .and_then(Value::as_u64)
        .unwrap_or(0)
}

fn publish(path: &Path, bytes: &[u8]) -> Result<(), String> {
    let tmp = path.with_extension("tmp");
    let result = (|| -> std::io::Result<()> {
        let mut file = private_file(&tmp)?;
        file.write_all(bytes)?;
        file.sync_all()?;
        atomic_file::replace(&tmp, path, true)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&tmp);
    }
    result.map_err(|e| e.to_string())
}

pub(crate) fn read_at(root: &Path) -> Result<Option<Value>, String> {
    let _lock = lock(root)?;
    read_manifest(root)
}

pub(crate) fn commit_at(root: &Path, mut manifest: Value, expected: u64) -> Result<CommitOutcome, String> {
    let _lock = lock(root)?;
    let current = read_manifest(root)?;
    if revision(&current) != expected {
        return Ok(CommitOutcome { committed: false, manifest: current });
    }
    let object = manifest
        .as_object_mut()
        .ok_or("A Home manifest must be an object.")?;
    object.insert("format".into(), Value::from(1));
    object.insert("revision".into(), Value::from(expected + 1));
    let bytes = serde_json::to_vec(&manifest).map_err(|e| e.to_string())?;
    if bytes.len() as u64 > MAX_MANIFEST_BYTES {
        return Err("Saved Home ideas exceed their storage limit.".into());
    }
    publish(&root.join(MANIFEST), &bytes)?;
    Ok(CommitOutcome { committed: true, manifest: Some(manifest) })
}

pub(crate) fn put_blob_at(root: &Path, item_id: &str, bytes: &[u8]) -> Result<(), String> {
    if !valid_item_id(item_id) {
        return Err("Invalid Home attachment identity.".into());
    }
    if bytes.len() as u64 > MAX_BLOB_BYTES {
        return Err("This attachment is too large to keep for recovery.".into());
    }
    let _lock = lock(root)?;
    let blobs = root.join(BLOBS);
    crate::config_dir::ensure_private_dir(&blobs).map_err(|e| e.to_string())?;
    publish(&blobs.join(item_id), bytes)
}

pub(crate) fn read_blob_at(root: &Path, item_id: &str) -> Result<Option<Vec<u8>>, String> {
    if !valid_item_id(item_id) {
        return Err("Invalid Home attachment identity.".into());
    }
    let _lock = lock(root)?;
    match fs::read(root.join(BLOBS).join(item_id)) {
        Ok(bytes) => Ok(Some(bytes)),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(error.to_string()),
    }
}

fn reachable(manifest: &Option<Value>) -> HashSet<String> {
    let mut ids = HashSet::new();
    let Some(manifest) = manifest else { return ids };
    let mut chip_lists: Vec<&Value> = Vec::new();
    if let Some(chips) = manifest.pointer("/composition/chips") {
        chip_lists.push(chips);
    }
    if let Some(launches) = manifest.get("launches").and_then(Value::as_array) {
        chip_lists.extend(launches.iter().filter_map(|launch| launch.get("chips")));
    }
    for chips in chip_lists {
        for chip in chips.as_array().into_iter().flatten() {
            if chip.get("kind").and_then(Value::as_str) == Some("bytes") {
                if let Some(id) = chip.get("itemId").and_then(Value::as_str) {
                    ids.insert(id.to_owned());
                }
            }
        }
    }
    ids
}

/// Removes staged bytes no saved record names, read under the writer lock so a
/// concurrent commit cannot make a removed item reachable.
pub(crate) fn sweep_at(root: &Path) -> Result<usize, String> {
    let _lock = lock(root)?;
    let keep = reachable(&read_manifest(root)?);
    let entries = match fs::read_dir(root.join(BLOBS)) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(0),
        Err(error) => return Err(error.to_string()),
    };
    let mut removed = 0;
    for entry in entries {
        let entry = entry.map_err(|e| e.to_string())?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if keep.contains(&name) {
            continue;
        }
        if fs::remove_file(entry.path()).is_ok() {
            removed += 1;
        }
    }
    Ok(removed)
}

pub(crate) fn fingerprint_of(path: &Path) -> Option<Fingerprint> {
    let info = fs::metadata(path).ok().filter(|info| info.is_file())?;
    let modified_ms = info
        .modified()
        .ok()
        .and_then(|time| time.duration_since(UNIX_EPOCH).ok())
        .map(|elapsed| elapsed.as_millis() as u64)
        .unwrap_or(0);
    Some(Fingerprint { size: info.len(), modified_ms })
}

#[tauri::command]
pub async fn read_home_outbox() -> Result<Option<Value>, String> {
    tauri::async_runtime::spawn_blocking(|| root().and_then(|root| read_at(&root)))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn commit_home_outbox(manifest: Value, expected_revision: u64) -> Result<CommitOutcome, String> {
    tauri::async_runtime::spawn_blocking(move || root().and_then(|root| commit_at(&root, manifest, expected_revision)))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn put_home_outbox_blob(request: tauri::ipc::Request<'_>) -> Result<(), String> {
    let item_id = request
        .headers()
        .get(ITEM_HEADER)
        .and_then(|value| value.to_str().ok())
        .ok_or("Missing Home attachment identity.")?
        .to_owned();
    let tauri::ipc::InvokeBody::Raw(bytes) = request.body() else {
        return Err("Home attachment bytes must be sent raw.".into());
    };
    let bytes = bytes.clone();
    tauri::async_runtime::spawn_blocking(move || root().and_then(|root| put_blob_at(&root, &item_id, &bytes)))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn read_home_outbox_blob(item_id: String) -> Result<tauri::ipc::Response, String> {
    let bytes = tauri::async_runtime::spawn_blocking(move || root().and_then(|root| read_blob_at(&root, &item_id)))
        .await
        .map_err(|e| e.to_string())??;
    match bytes {
        Some(bytes) => Ok(tauri::ipc::Response::new(bytes)),
        None => Err("missing".into()),
    }
}

#[tauri::command]
pub async fn sweep_home_outbox() -> Result<usize, String> {
    tauri::async_runtime::spawn_blocking(|| root().and_then(|root| sweep_at(&root)))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn fingerprint_home_path(path: String) -> Result<Option<Fingerprint>, String> {
    tauri::async_runtime::spawn_blocking(move || Ok(fingerprint_of(Path::new(&path))))
        .await
        .map_err(|e| e.to_string())?
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;
    use serde_json::json;

    const ITEM: &str = "0b6f8c1e-6c0d-4bb4-9f6a-0f3e2b9a7c11";

    #[test]
    fn commits_only_against_the_revision_it_read() {
        let dir = TempDir::new("home-outbox-commit");
        let root = dir.join("home");
        let first = commit_at(&root, json!({"launches": []}), 0).expect("first commit");
        assert!(first.committed);
        let stale = commit_at(&root, json!({"launches": [{"launchId": "x"}]}), 0).expect("stale commit");
        assert!(!stale.committed);
        assert_eq!(stale.manifest.and_then(|m| m["revision"].as_u64()), Some(1));
        let next = commit_at(&root, json!({"launches": []}), 1).expect("next commit");
        assert!(next.committed);
        assert_eq!(read_at(&root).expect("read").and_then(|m| m["revision"].as_u64()), Some(2));
    }

    #[test]
    fn refuses_an_unknown_manifest_without_touching_it() {
        let dir = TempDir::new("home-outbox-format");
        let root = dir.join("home");
        fs::create_dir_all(&root).expect("mkdir");
        fs::write(root.join(MANIFEST), br#"{"format":9,"revision":3}"#).expect("write");
        assert!(read_at(&root).is_err());
        assert!(commit_at(&root, json!({}), 3).is_err());
        assert_eq!(fs::read(root.join(MANIFEST)).expect("read"), br#"{"format":9,"revision":3}"#);
    }

    #[test]
    fn sweeps_bytes_no_record_names() {
        let dir = TempDir::new("home-outbox-sweep");
        let root = dir.join("home");
        let orphan = "1b6f8c1e-6c0d-4bb4-9f6a-0f3e2b9a7c11";
        put_blob_at(&root, ITEM, b"kept").expect("put kept");
        put_blob_at(&root, orphan, b"orphan").expect("put orphan");
        commit_at(
            &root,
            json!({"launches": [{"chips": [{"kind": "bytes", "itemId": ITEM}, {"kind": "path", "itemId": orphan}]}]}),
            0,
        )
        .expect("commit");
        assert_eq!(sweep_at(&root).expect("sweep"), 1);
        assert_eq!(read_blob_at(&root, ITEM).expect("read kept"), Some(b"kept".to_vec()));
        assert_eq!(read_blob_at(&root, orphan).expect("read orphan"), None);
    }

    #[test]
    fn refuses_item_ids_that_could_leave_the_store() {
        let dir = TempDir::new("home-outbox-ids");
        let root = dir.join("home");
        assert!(put_blob_at(&root, "../manifest.json", b"x").is_err());
        assert!(read_blob_at(&root, "..").is_err());
    }

    #[test]
    fn fingerprints_files_but_not_folders() {
        let dir = TempDir::new("home-outbox-fingerprint");
        let file = dir.join("idea.txt");
        fs::write(&file, b"twelve bytes").expect("write");
        assert_eq!(fingerprint_of(&file).map(|f| f.size), Some(12));
        assert!(fingerprint_of(&dir.join("missing")).is_none());
        assert!(fingerprint_of(dir.path()).is_none());
    }
}
