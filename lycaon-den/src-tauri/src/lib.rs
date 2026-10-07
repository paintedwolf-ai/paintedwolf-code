mod accessibility_text_size;
mod app_exit;
mod app_menu;
mod app_state_views;
mod atomic_file;
mod backup_transfer;
mod backup_transfer_journal;
mod clipboard;
mod config_dir;
mod detect_editors;
mod document_outbox;
mod external_attachment_import;
mod item_windows;
#[cfg(target_os = "macos")]
mod native_notifications;
mod open_external;
mod open_local_path;
mod open_third_party_notices;
mod pick_path;
mod picked_file;
mod presence;
mod read_path_bytes;
mod reveal_in_file_manager;
mod shared_composer_documents;
mod shell_command;
mod sidecar;
mod system_appearance;
#[cfg(test)]
mod test_support;
pub mod update_service;
mod vault_lock;
pub mod webkit_features;
mod webview_policy;
pub mod wheel_smoothing;
mod window_appearance;
mod window_backdrop;

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{LazyLock, Mutex};

use sidecar::SidecarState;
use tauri::{Emitter, Manager, RunEvent};

static APP_STATE_WRITER: LazyLock<Mutex<()>> = LazyLock::new(|| Mutex::new(()));

const APP_STATE_DIR: &str = "app-state-v1";
const APP_STATE_SLICE_SUFFIX: &str = ".json";
const APP_STATE_VERSION: u64 = 1;

pub(crate) fn den_state_dir() -> Option<PathBuf> {
    config_dir::host_config_dir().ok()
}

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
async fn read_app_state() -> Option<serde_json::Value> {
    let dir = den_state_dir()?;
    tauri::async_runtime::spawn_blocking(move || {
        read_app_state_doc(&dir).ok().map(serde_json::Value::Object)
    })
    .await
    .ok()
    .flatten()
}

/// Runs app-state writes off the main thread.
#[tauri::command]
async fn patch_app_state(
    app: tauri::AppHandle,
    window: tauri::Window,
    patch: serde_json::Map<String, serde_json::Value>,
) -> Result<(), String> {
    let dir = den_state_dir().ok_or_else(|| "no home dir".to_string())?;
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
            let merged = app_state_views::merge_window_intents(current, requested, &label)?;
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

/// Redeems a save-panel grant before writing to its selected destination.
#[tauri::command]
fn save_bytes(grant: String, bytes: Vec<u8>) -> Result<(), String> {
    let dest = picked_file::redeem(&grant, picked_file::GrantMode::Write)?;
    picked_file::save_atomic(&dest, bytes.as_slice())
}

/// Runs notification work off the main thread.
#[cfg(target_os = "macos")]
async fn await_un<T, F>(work: F) -> Result<T, String>
where
    T: Send + 'static,
    F: FnOnce() -> Result<T, String> + Send + 'static,
{
    tauri::async_runtime::spawn_blocking(work)
        .await
        .map_err(|e| format!("notification request did not complete: {e}"))?
}

#[tauri::command]
async fn den_notify_permission_state() -> Result<String, String> {
    #[cfg(target_os = "macos")]
    {
        await_un(|| native_notifications::authorization_state().map(|s| s.as_str().to_string()))
            .await
    }
    #[cfg(not(target_os = "macos"))]
    {
        Ok("denied".to_string())
    }
}

#[tauri::command]
async fn den_notify_request_permission() -> Result<String, String> {
    #[cfg(target_os = "macos")]
    {
        await_un(|| native_notifications::request_authorization().map(|s| s.as_str().to_string()))
            .await
    }
    #[cfg(not(target_os = "macos"))]
    {
        Ok("denied".to_string())
    }
}

#[tauri::command]
async fn den_notify_send(
    title: String,
    body: String,
    session_id: Option<String>,
) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        await_un(move || native_notifications::send(&title, &body, session_id.as_deref())).await
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (title, body, session_id);
        Ok(())
    }
}

/// Nonpositive counts clear the dock badge.
#[tauri::command]
fn den_set_badge_count(app: tauri::AppHandle, count: i64) -> Result<(), String> {
    let Some(window) = app.get_webview_window("main") else {
        return Err("Main window is unavailable".into());
    };
    let value = if count > 0 { Some(count) } else { None };
    window.set_badge_count(value).map_err(|e| e.to_string())
}

#[tauri::command]
async fn den_notify_cancel_sessions(session_ids: Vec<String>) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        await_un(move || native_notifications::cancel_sessions(&session_ids)).await
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = session_ids;
        Ok(())
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    // The launch guard runs before any window so an interrupted update finishes first.
    let launch = match update_service::transaction::launch() {
        Ok(launch) => launch,
        Err(error) => {
            rfd::MessageDialog::new()
                .set_title("Update recovery needed")
                .set_description(update_service::transaction::recovery_guidance(&error))
                .set_level(rfd::MessageLevel::Error)
                .show();
            return;
        }
    };
    #[cfg(not(target_os = "macos"))]
    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_notification::init());
    #[cfg(target_os = "macos")]
    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_shell::init());
    builder
        .plugin(webview_policy::init())
        .manage(SidecarState::new())
        .manage(app_exit::ExitCoordinator::default())
        .manage(backup_transfer::BackupTransferState::default())
        .manage(update_service::UpdateService::new(
            env!("PAINTED_WOLF_VERSION").to_string(),
            launch,
        ))
        .invoke_handler(tauri::generate_handler![
            app_exit::pending_app_exit,
            app_exit::acknowledge_app_exit,
            document_outbox::list_document_outbox,
            document_outbox::inspect_document_outbox,
            document_outbox::read_document_outbox,
            document_outbox::commit_document_outbox,
            read_app_state,
            patch_app_state,
            save_bytes,
            backup_transfer::transfer_backup,
            backup_transfer::cancel_backup_transfer,
            den_notify_permission_state,
            den_notify_request_permission,
            den_notify_send,
            den_notify_cancel_sessions,
            den_set_badge_count,
            accessibility_text_size::accessibility_preferred_text_size,
            system_appearance::system_appearance,
            clipboard::read_clipboard_text,
            clipboard::write_clipboard_text,
            clipboard::paste_into_focused_view,
            reveal_in_file_manager::reveal_in_file_manager,
            reveal_in_file_manager::reveal_typed_path_in_file_manager,
            read_path_bytes::path_kind,
            read_path_bytes::import_path_kind,
            read_path_bytes::read_path_bytes,
            external_attachment_import::import_external_attachment,
            pick_path::pick_path,
            open_external::open_in_editor,
            open_external::open_in_browser,
            open_local_path::open_local_path,
            detect_editors::detect_editors,
            open_third_party_notices::open_third_party_notices,
            update_service::preferences::get_update_state,
            update_service::preferences::set_automatic_updates_enabled,
            update_service::preferences::set_update_channel,
            update_service::check::check_update,
            update_service::download::download_update,
            update_service::download::retry_update,
            update_service::transaction::restart_to_update,
            shell_command::shell_command_status,
            shell_command::install_shell_command,
            shell_command::uninstall_shell_command,
            item_windows::ipc_open_item_window,
            item_windows::drag::ipc_begin_file_item_window_drag,
            item_windows::drag::ipc_move_item_window_drag,
            item_windows::drag::ipc_finish_item_window_drag,
            item_windows::drag::ipc_cancel_item_window_drag,
            item_windows::drag::ipc_discard_pending_item_window_drags,
            item_windows::drag::ipc_update_file_tab_drop_target,
            item_windows::drag::ipc_clear_file_tab_drop_target,
            item_windows::ipc_list_item_window_views,
            item_windows::presentation::ipc_list_workspace_view_presentations,
            item_windows::presentation::ipc_set_workspace_view_contexts,
            item_windows::ipc_focus_item_window,
            item_windows::ipc_close_item_window,
            shared_composer_documents::resolve_shared_composer_document,
            shared_composer_documents::acquire_shared_composer_document_lease,
            shared_composer_documents::update_shared_composer_document_draft,
            shared_composer_documents::apply_shared_composer_document_mutation,
            shared_composer_documents::protect_shared_composer_selection,
            shared_composer_documents::remove_shared_composer_document_attachment,
            shared_composer_documents::clear_shared_composer_document,
            sidecar::commands::ipc_start_sidecar,
            sidecar::commands::ipc_export_startup_diagnostics,
            sidecar::commands::ipc_restart_sidecar,
            sidecar::commands::ipc_reset_credential_vault,
            sidecar::commands::ipc_cancel_sidecar_start,
            sidecar::commands::ipc_attach_existing_daemon,
            sidecar::commands::ipc_sidecar_info,
            sidecar::commands::ipc_engine_state,
            presence::ipc_reveal_managed_secret,
            presence::ipc_resolve_checkpoint_with_presence,
            window_appearance::den_reveal_window,
            window_appearance::den_set_window_bounds,
            window_backdrop::den_set_window_backdrop,
            app_menu::set_app_menu,
        ])
        .on_menu_event(|app, event| {
            app_menu::on_menu_event(app, event.id().as_ref());
        })
        .setup(|app| {
            backup_transfer_journal::cleanup_on_launch();
            document_outbox::reconcile_windows(app.handle(), None);
            sidecar::layout::setup_bundled_engine_layout(app.handle());
            sidecar::setup_supervision(app.handle());
            webkit_features::create_main_window(app.handle())?;
            window_appearance::setup(app.handle());
            update_service::startup::start(app.handle().clone());
            accessibility_text_size::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            system_appearance::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            wheel_smoothing::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            vault_lock::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            #[cfg(target_os = "macos")]
            {
                native_notifications::setup(app.handle().clone())
                    .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            }
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while running tauri application")
        .run(|app, event| match event {
            RunEvent::ExitRequested { api, code, .. } => {
                if app_exit::allow_exit(app, code) {
                    window_appearance::prepare_to_quit();
                } else {
                    api.prevent_exit();
                }
            }
            #[cfg(target_os = "macos")]
            RunEvent::Reopen {
                has_visible_windows: false,
                ..
            } => window_appearance::restore_main_workspace(app),
            RunEvent::Exit => {
                if let Some(state) = app.try_state::<SidecarState>() {
                    // Record why the unlocks ended before the engine stops.
                    vault_lock::lock_now(&state, vault_lock::LockReason::AppQuit);
                    sidecar::stop_sidecar(&state);
                }
            }
            _ => {}
        });
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;
    use std::path::Path;

    fn command_js_name(src: &str, func: &str) -> String {
        let Some(at) = src.find(&format!("fn {func}(")) else {
            return func.to_string();
        };
        let preceding = &src[at.saturating_sub(160)..at];
        let Some(attr) = preceding.rfind("#[tauri::command") else {
            return func.to_string();
        };
        let attr_text = &preceding[attr..];
        let Some(rename) = attr_text.find("rename = \"") else {
            return func.to_string();
        };
        let rest = &attr_text[rename + "rename = \"".len()..];
        match rest.find('"') {
            Some(end) => rest[..end].to_string(),
            None => func.to_string(),
        }
    }

    /// `commands.allow` entry → the permission identifier that grants it.
    fn permission_by_command(perm_dir: &Path) -> std::collections::HashMap<String, String> {
        let mut out = std::collections::HashMap::new();
        let Ok(entries) = fs::read_dir(perm_dir) else {
            return out;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.extension().and_then(|e| e.to_str()) != Some("toml") {
                continue;
            }
            let Ok(text) = fs::read_to_string(&path) else {
                continue;
            };
            let mut identifier: Option<String> = None;
            for line in text.lines() {
                let line = line.trim();
                if let Some(rest) = line.strip_prefix("identifier = \"") {
                    identifier = rest.strip_suffix('"').map(str::to_string);
                } else if let Some(rest) = line.strip_prefix("commands.allow = [") {
                    let Some(id) = identifier.clone() else {
                        continue;
                    };
                    for name in rest.trim_end_matches(']').split(',') {
                        let name = name.trim().trim_matches('"');
                        if !name.is_empty() {
                            out.insert(name.to_string(), id.clone());
                        }
                    }
                }
            }
        }
        out
    }

    /// Verifies command permissions and the default capability stay aligned.
    #[test]
    fn every_invoke_command_is_reachable_from_the_default_capability() {
        let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let lib_src = fs::read_to_string(root.join("src/lib.rs")).expect("read lib.rs");

        let marker = "tauri::generate_handler![";
        let start = lib_src.find(marker).expect("generate_handler! block") + marker.len();
        let rest = &lib_src[start..];
        let end = rest.find(']').expect("generate_handler! close");

        let capabilities =
            fs::read_to_string(root.join("capabilities/default.json")).expect("read default.json");
        let allowed = permission_by_command(&root.join("permissions"));

        let mut unreachable = Vec::new();
        for line in rest[..end].lines() {
            let entry = line.trim().trim_end_matches(',');
            if entry.is_empty() || entry.starts_with("//") {
                continue;
            }
            let (module, func) = match entry.rsplit_once("::") {
                Some((module, func)) => (module, func),
                None => ("lib", entry),
            };
            let module_src = if module == "lib" {
                lib_src.clone()
            } else {
                fs::read_to_string(root.join(format!("src/{}.rs", module.replace("::", "/"))))
                    .unwrap_or_else(|_| panic!("read src/{module}.rs for {entry}"))
            };
            let name = command_js_name(&module_src, func);
            match allowed.get(&name) {
                None => unreachable.push(format!("{name}: no permissions/*.toml declares it")),
                Some(id) => {
                    if !capabilities.contains(&format!("\"{id}\"")) {
                        unreachable.push(format!(
                            "{name}: permission {id} is missing from capabilities/default.json"
                        ));
                    }
                }
            }
        }
        assert!(
            unreachable.is_empty(),
            "commands callable from JS but denied at runtime: {unreachable:#?}"
        );
    }

    #[test]
    fn badge_permission_is_main_window_only() {
        let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let permission = "allow-den-set-badge-count";
        let allowed = permission_by_command(&root.join("permissions"));
        assert_eq!(
            allowed.get("den_set_badge_count").map(String::as_str),
            Some(permission)
        );
        for entry in fs::read_dir(root.join("capabilities")).expect("read capabilities") {
            let path = entry.expect("capability entry").path();
            if path.extension().and_then(|ext| ext.to_str()) != Some("json") {
                continue;
            }
            let raw = fs::read_to_string(&path).expect("read capability");
            let capability: serde_json::Value =
                serde_json::from_str(&raw).expect("parse capability");
            let grants_badge = capability["permissions"]
                .as_array()
                .expect("permissions array")
                .iter()
                .any(|entry| {
                    entry.as_str() == Some(permission)
                        || entry["identifier"].as_str() == Some(permission)
                });
            if path.file_name().and_then(|name| name.to_str()) == Some("default.json") {
                assert!(grants_badge);
                assert_eq!(capability["windows"], serde_json::json!(["main"]));
            } else {
                assert!(
                    !grants_badge,
                    "badge permission escaped main capability: {}",
                    path.display()
                );
            }
        }
    }

    #[test]
    fn durable_close_can_destroy_every_window_it_intercepts() {
        let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        for name in ["default.json", "items.json"] {
            let raw = fs::read_to_string(root.join("capabilities").join(name))
                .unwrap_or_else(|_| panic!("read {name}"));
            let capability: serde_json::Value =
                serde_json::from_str(&raw).unwrap_or_else(|_| panic!("parse {name}"));
            let permissions = capability["permissions"]
                .as_array()
                .unwrap_or_else(|| panic!("permissions array in {name}"));
            assert!(
                permissions
                    .iter()
                    .any(|permission| permission == "core:window:allow-destroy"),
                "{name} must allow the durable close handler to destroy its window"
            );
        }
    }

    /// Prevents process-wide home overrides in parallel tests.
    #[test]
    fn no_source_file_sets_home_env_var() {
        let src_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("src");
        let mut offenders = Vec::new();
        for entry in fs::read_dir(&src_dir).expect("read src dir") {
            let path = entry.expect("src dir entry").path();
            if path.extension().and_then(|e| e.to_str()) != Some("rs") {
                continue;
            }
            let body = fs::read_to_string(&path).expect("read source file");
            for var in ["HOME", "USERPROFILE"] {
                if body.contains(&format!("set_var(\"{var}\"")) {
                    offenders.push(format!("{} sets {var}", path.display()));
                }
            }
        }
        assert!(
            offenders.is_empty(),
            "env vars that path resolution reads must not be mutated:\n  {}",
            offenders.join("\n  ")
        );
    }

    fn seed_file(dir: &std::path::Path, json: &str) {
        let mut doc = serde_json::from_str::<serde_json::Map<String, serde_json::Value>>(json)
            .expect("parse app-state seed");
        doc.remove("version");
        apply_app_state_patch_at(dir, &doc).expect("seed app-state");
    }

    fn read_file(dir: &std::path::Path) -> String {
        serde_json::to_string(&read_app_state_doc(dir).expect("read app-state"))
            .expect("serialize app-state")
    }

    fn map(pairs: &[(&str, serde_json::Value)]) -> serde_json::Map<String, serde_json::Value> {
        pairs
            .iter()
            .map(|(k, v)| ((*k).to_string(), v.clone()))
            .collect()
    }

    #[test]
    fn patch_merges_slices_without_clobber() {
        let dir = TempDir::new("app-state-merge");
        seed_file(&dir, r#"{"version":1,"recents":[]}"#);
        let display = map(&[("display", serde_json::json!({"diffWordWrap": false}))]);
        patch_app_state_at(&dir, &display).expect("display patch");
        let layout = map(&[("layout", serde_json::json!({"navCollapsed": true}))]);
        let merged = patch_app_state_at(&dir, &layout).expect("layout patch");
        assert_eq!(merged["version"], 1);
        assert_eq!(merged["display"]["diffWordWrap"], false);
        assert_eq!(merged["layout"]["navCollapsed"], true);
        let on_disk: serde_json::Value = serde_json::from_str(&read_file(&dir)).expect("parse");
        assert_eq!(on_disk["display"]["diffWordWrap"], false);
        assert_eq!(on_disk["layout"]["navCollapsed"], true);
        assert_eq!(on_disk["version"], 1);
    }

    #[test]
    fn patch_writes_only_the_named_slice() {
        let dir = TempDir::new("app-state-slice-isolation");
        seed_file(
            &dir,
            r#"{"version":1,"display":{"diffWordWrap":true},"layout":{"navCollapsed":false}}"#,
        );
        let layout_path = app_state_slices_dir(&dir).join(app_state_slice_name("layout"));
        let layout_before = fs::read(&layout_path).expect("read layout slice");

        patch_app_state_at(
            &dir,
            &map(&[("display", serde_json::json!({"diffWordWrap": false}))]),
        )
        .expect("patch display");

        assert_eq!(
            fs::read(&layout_path).expect("read layout slice after"),
            layout_before
        );
    }

    #[test]
    fn patch_null_deletes_a_slice() {
        let dir = TempDir::new("app-state-null-delete");
        seed_file(
            &dir,
            r#"{"version":1,"display":{"diffWordWrap":true},"layout":{"navCollapsed":false}}"#,
        );
        let patch = map(&[("display", serde_json::Value::Null)]);
        let merged = patch_app_state_at(&dir, &patch).expect("null delete");
        assert!(merged.get("display").is_none());
        assert_eq!(merged["layout"]["navCollapsed"], false);
        assert_eq!(merged["version"], 1);
    }

    #[test]
    fn patch_replaces_slice_wholesale() {
        let dir = TempDir::new("app-state-wholesale");
        seed_file(
            &dir,
            r#"{"shortcuts":{"overrides":{"a":"Mod+A","b":"Mod+B"}}}"#,
        );
        let patch = map(&[("shortcuts", serde_json::json!({"overrides":{"a":"Mod+A"}}))]);
        let merged = patch_app_state_at(&dir, &patch).expect("wholesale replace");
        assert_eq!(merged["version"], APP_STATE_VERSION);
        assert_eq!(merged["shortcuts"]["overrides"]["a"], "Mod+A");
        assert!(merged["shortcuts"]["overrides"].get("b").is_none());
    }

    #[test]
    fn patch_refuses_version() {
        let dir = TempDir::new("app-state-refuse-version");
        let patch = map(&[("version", serde_json::json!(99))]);
        let err = patch_app_state_at(&dir, &patch).expect_err("version refused");
        assert_eq!(err, "app_state_version_immutable");
        assert!(!app_state_slices_dir(&dir).exists());

        seed_file(&dir, r#"{"version":1,"recents":[]}"#);
        let before = read_file(&dir);
        let err = patch_app_state_at(&dir, &patch).expect_err("version refused with file");
        assert_eq!(err, "app_state_version_immutable");
        assert_eq!(read_file(&dir), before);
    }

    #[test]
    fn patch_accepts_unknown_top_level_key() {
        let dir = TempDir::new("app-state-unknown-key");
        let patch = map(&[("someFutureSlice", serde_json::json!({"x": 1}))]);
        let merged = patch_app_state_at(&dir, &patch).expect("unknown key");
        assert_eq!(merged["someFutureSlice"]["x"], 1);
        let on_disk: serde_json::Value = serde_json::from_str(&read_file(&dir)).expect("parse");
        assert_eq!(on_disk["someFutureSlice"]["x"], 1);
    }

    #[test]
    fn unreadable_slice_does_not_hide_healthy_state() {
        let dir = TempDir::new("app-state-unreadable");
        seed_file(&dir, r#"{"version":1,"layout":{"navCollapsed":true}}"#);
        fs::write(app_state_slices_dir(&dir).join("broken.json"), "not json")
            .expect("write broken slice");
        let doc = read_app_state_doc(&dir).expect("read healthy slices");
        assert_eq!(doc["layout"]["navCollapsed"], true);
    }

    #[test]
    fn mismatched_slice_envelope_is_ignored() {
        let dir = TempDir::new("app-state-mismatched-envelope");
        seed_file(&dir, r#"{"version":1,"layout":{"navCollapsed":true}}"#);
        fs::write(
            app_state_slices_dir(&dir).join(app_state_slice_name("debug")),
            r#"{"key":"layout","value":{"navCollapsed":false}}"#,
        )
        .expect("write mismatched slice");

        let doc = read_app_state_doc(&dir).expect("read healthy slices");
        assert_eq!(doc["layout"]["navCollapsed"], true);
        assert!(doc.get("debug").is_none());
    }

    #[test]
    fn patch_leaves_no_temp_file() {
        let dir = TempDir::new("app-state-no-temp");
        seed_file(&dir, r#"{"version":1}"#);
        let before = read_file(&dir);
        let patch = map(&[("display", serde_json::json!({"diffWordWrap": true}))]);
        patch_app_state_at(&dir, &patch).expect("success");
        let slices = app_state_slices_dir(&dir);
        assert!(fs::read_dir(&slices)
            .expect("read slices")
            .all(|entry| !entry
                .expect("slice entry")
                .path()
                .to_string_lossy()
                .ends_with(".tmp")));

        // Make temporary-file creation fail.
        let fail_dir = TempDir::new("app-state-temp-as-dir");
        seed_file(&fail_dir, &before);
        let before_fail = read_file(&fail_dir);
        let name = app_state_slice_name("layout");
        let tmp = app_state_slices_dir(&fail_dir).join(format!("{name}.tmp"));
        fs::create_dir_all(app_state_slices_dir(&fail_dir)).expect("slice fixture dir");
        fs::create_dir(&tmp).expect("tmp as dir");
        let err = patch_app_state_at(
            &fail_dir,
            &map(&[("layout", serde_json::json!({"navCollapsed": true}))]),
        )
        .expect_err("create fails when tmp is a directory");
        assert!(!err.is_empty());
        assert_eq!(read_file(&fail_dir), before_fail);
        // Remove the fixture directory before checking for a file.
        if tmp.is_dir() {
            let _ = fs::remove_dir_all(&tmp);
        }
        assert!(!tmp.is_file());
    }

    #[test]
    fn patch_creates_file_when_absent() {
        let dir = TempDir::new("app-state-create");
        let patch = map(&[("recents", serde_json::json!([]))]);
        let merged = patch_app_state_at(&dir, &patch).expect("create");
        assert!(app_state_slices_dir(&dir)
            .join(app_state_slice_name("recents"))
            .exists());
        assert_eq!(merged["version"], APP_STATE_VERSION);
        assert_eq!(merged["recents"], serde_json::json!([]));
        let on_disk: serde_json::Value = serde_json::from_str(&read_file(&dir)).expect("parse");
        assert_eq!(on_disk["version"], APP_STATE_VERSION);
        assert_eq!(on_disk["recents"], serde_json::json!([]));
    }

    #[test]
    fn byte_commands_refuse_an_unminted_grant() {
        save_bytes("forged-handle".to_string(), b"content".to_vec())
            .expect_err("save_bytes must refuse a handle it never minted");
    }

    #[test]
    fn a_write_grant_round_trips_and_does_not_replay() {
        let dir = TempDir::new("app-state-byte-commands");
        let target = dir.join("nested").join("export.bin");
        let grant =
            picked_file::mint(&target, picked_file::GrantMode::Write).expect("mint write grant");

        save_bytes(grant.clone(), b"payload".to_vec()).expect("save through grant");
        assert_eq!(fs::read(&target).expect("read back"), b"payload".to_vec());

        // A spent handle cannot write again.
        save_bytes(grant, b"second".to_vec()).expect_err("grant is single-use");
        assert_eq!(fs::read(&target).expect("unchanged"), b"payload".to_vec());
    }

    #[test]
    fn a_read_grant_cannot_be_used_to_write() {
        let dir = TempDir::new("app-state-byte-commands-direction");
        let target = dir.join("readonly.bin");
        fs::write(&target, b"original").expect("seed file");

        let grant =
            picked_file::mint(&target, picked_file::GrantMode::Read).expect("mint read grant");
        save_bytes(grant, b"overwritten".to_vec())
            .expect_err("a read pick may not be written through");
        assert_eq!(fs::read(&target).expect("unchanged"), b"original".to_vec());
    }
}

/// The installer mode never initializes windows, tools, or the engine.
pub fn run_update_helper(id: &str, installation: &str) -> Result<(), String> {
    update_service::initialize_helper(installation)?;
    update_service::transaction::run_helper(id).map_err(|e| e.to_string())
}
