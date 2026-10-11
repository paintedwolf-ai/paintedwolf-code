//! Device app-state slices and shared editing persistence.

pub(crate) mod views;
pub(crate) mod document_outbox;
pub(crate) mod shared_composer_documents;

use crate::{atomic_file, config_dir};
use tauri::Emitter;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{LazyLock, Mutex};

static APP_STATE_WRITER: LazyLock<Mutex<()>> = LazyLock::new(|| Mutex::new(()));

const APP_STATE_DIR: &str = "app-state-v1";
const APP_STATE_SLICE_SUFFIX: &str = ".json";
const APP_STATE_VERSION: u64 = 1;

fn cleanup_app_state_tmp(tmp: &Path, msg: String) -> String {
    let _ = fs::remove_file(tmp);
    msg
}

#[cfg(unix)]
fn create_private_app_state_file(path: &Path) -> std::io::Result<fs::File> {
    use std::os::unix::fs::OpenOptionsExt;

    fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
}

#[cfg(not(unix))]
fn create_private_app_state_file(path: &Path) -> std::io::Result<fs::File> {
    fs::File::create(path)
}

fn app_state_slices_dir(dir: &Path) -> PathBuf {
    dir.join(APP_STATE_DIR)
}

fn ensure_app_state_slices_dir(dir: &Path) -> std::io::Result<PathBuf> {
    let slices = app_state_slices_dir(dir);
    fs::create_dir_all(&slices)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&slices, fs::Permissions::from_mode(0o700))?;
    }
    Ok(slices)
}

fn app_state_slice_name(key: &str) -> String {
    let mut name = String::with_capacity(key.len() * 2 + APP_STATE_SLICE_SUFFIX.len());
    for byte in key.as_bytes() {
        use std::fmt::Write as _;
        let _ = write!(&mut name, "{byte:02x}");
    }
    name.push_str(APP_STATE_SLICE_SUFFIX);
    name
}

fn validate_app_state_patch(
    patch: &serde_json::Map<String, serde_json::Value>,
) -> Result<(), String> {
    if patch.contains_key("version") {
        return Err("app_state_version_immutable".into());
    }
    if patch.keys().any(|key| key.trim().is_empty()) {
        return Err("app_state_key_invalid".into());
    }
    Ok(())
}

fn read_app_state_doc(dir: &Path) -> Result<serde_json::Map<String, serde_json::Value>, String> {
    let mut doc = serde_json::Map::new();
    doc.insert("version".into(), serde_json::json!(APP_STATE_VERSION));
    let slices = app_state_slices_dir(dir);
    let entries = match fs::read_dir(&slices) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(doc),
        Err(error) => return Err(format!("app_state_read_failed: {error}")),
    };
    for entry in entries {
        let entry = entry.map_err(|error| format!("app_state_read_failed: {error}"))?;
        let path = entry.path();
        if path.extension().and_then(|ext| ext.to_str()) != Some("json") {
            continue;
        }
        let raw = match fs::read_to_string(&path) {
            Ok(raw) => raw,
            Err(_) => continue,
        };
        let envelope = match serde_json::from_str::<serde_json::Value>(&raw) {
            Ok(serde_json::Value::Object(envelope)) => envelope,
            _ => continue,
        };
        let Some(key) = envelope.get("key").and_then(serde_json::Value::as_str) else {
            continue;
        };
        if key.is_empty() || key == "version" {
            continue;
        }
        let expected_name = app_state_slice_name(key);
        if path.file_name().and_then(|name| name.to_str()) != Some(expected_name.as_str()) {
            continue;
        }
        if let Some(value) = envelope.get("value") {
            doc.insert(key.to_string(), value.clone());
        }
    }
    Ok(doc)
}

fn app_state_slice_requires_sync(key: &str) -> bool {
    matches!(
        key,
        "composerDrafts" | "composerAttachments" | "filesHotExit" | "filesTreeIntent"
    )
}

fn write_app_state_slice(dir: &Path, key: &str, value: &serde_json::Value) -> Result<(), String> {
    let raw = serde_json::to_vec(&serde_json::json!({"key": key, "value": value}))
        .map_err(|error| error.to_string())?;
    config_dir::ensure_private_dir(dir).map_err(|e| e.to_string())?;
    let slices = ensure_app_state_slices_dir(dir).map_err(|error| error.to_string())?;
    let name = app_state_slice_name(key);
    let path = slices.join(&name);
    let tmp = slices.join(format!("{name}.tmp"));
    let mut f = create_private_app_state_file(&tmp)
        .map_err(|e| cleanup_app_state_tmp(&tmp, e.to_string()))?;
    f.write_all(&raw)
        .map_err(|e| cleanup_app_state_tmp(&tmp, e.to_string()))?;
    let durable = app_state_slice_requires_sync(key);
    if durable {
        f.sync_data()
            .map_err(|e| cleanup_app_state_tmp(&tmp, e.to_string()))?;
    }
    drop(f);
    atomic_file::replace(&tmp, &path, durable)
        .map_err(|error| cleanup_app_state_tmp(&tmp, error.to_string()))
}

fn delete_app_state_slice(dir: &Path, key: &str) -> Result<(), String> {
    let path = app_state_slices_dir(dir).join(app_state_slice_name(key));
    match fs::remove_file(&path) {
        Ok(()) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error.to_string()),
    }
    if app_state_slice_requires_sync(key) {
        let parent = path
            .parent()
            .ok_or_else(|| "app_state_slice_has_no_parent".to_string())?;
        fs::File::open(parent)
            .and_then(|directory| directory.sync_all())
            .map_err(|error| error.to_string())?;
    }
    Ok(())
}

fn apply_app_state_patch_at(
    dir: &Path,
    patch: &serde_json::Map<String, serde_json::Value>,
) -> Result<(), String> {
    validate_app_state_patch(patch)?;
    for (key, value) in patch {
        if value.is_null() {
            delete_app_state_slice(dir, key)?;
        } else {
            write_app_state_slice(dir, key, value)?;
        }
    }
    Ok(())
}

#[cfg(test)]
fn patch_app_state_at(
    dir: &Path,
    patch: &serde_json::Map<String, serde_json::Value>,
) -> Result<serde_json::Value, String> {
    apply_app_state_patch_at(dir, patch)?;
    let doc = read_app_state_doc(dir)?;
    Ok(serde_json::Value::Object(doc))
}

#[tauri::command]
pub(crate) async fn read_app_state() -> Option<serde_json::Value> {
    let dir = config_dir::host_config_dir().ok()?;
    tauri::async_runtime::spawn_blocking(move || {
        read_app_state_doc(&dir).ok().map(serde_json::Value::Object)
    })
    .await
    .ok()
    .flatten()
}

/// Runs app-state writes off the main thread.
#[tauri::command]
pub(crate) async fn patch_app_state(
    app: tauri::AppHandle,
    window: tauri::Window,
    patch: serde_json::Map<String, serde_json::Value>,
) -> Result<(), String> {
    let dir = config_dir::host_config_dir().ok().ok_or_else(|| "no home dir".to_string())?;
    let label = window.label().to_string();
    let echo = tauri::async_runtime::spawn_blocking(move || {
        let _writer = APP_STATE_WRITER
            .lock()
            .map_err(|_| "app_state_lock_poisoned".to_string())?;
        let mut patch = patch;
        if let Some(requested) = patch.get("filesTreeIntent") {
            let path = app_state_slices_dir(&dir).join(app_state_slice_name("filesTreeIntent"));
            let current = match fs::read(path) {
                Ok(bytes) => serde_json::from_slice::<serde_json::Value>(&bytes)
                    .map_err(|error| error.to_string())?
                    .get("value")
                    .cloned()
                    .ok_or("invalid_saved_tree_configuration")?,
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                    serde_json::Value::Null
                }
                Err(error) => return Err(error.to_string()),
            };
            let merged = views::merge_window_intents(current, requested, &label)?;
            patch.insert("filesTreeIntent".to_string(), merged);
        }
        apply_app_state_patch_at(&dir, &patch)?;
        Ok::<_, String>(patch)
    })
    .await
    .map_err(|e| format!("app_state_write_did_not_complete: {e}"))??;
    // Peers receive only changed slices.
    let _ = app.emit(
        "app-state-changed",
        serde_json::json!({ "originLabel": window.label(), "patch": echo }),
    );
    Ok(())
}


#[cfg(test)]
mod tests;
