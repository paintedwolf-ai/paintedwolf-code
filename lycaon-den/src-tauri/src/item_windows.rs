//! Durable item windows and the host-managed workspace presentation registry.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{LazyLock, Mutex};

use serde::{Deserialize, Serialize};
use tauri::{
    AppHandle, Emitter, LogicalPosition, Manager, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};
#[cfg(target_os = "macos")]
use tauri::{Position, TitleBarStyle};

pub(crate) mod drag;
pub(crate) mod presentation;
#[cfg(test)]
mod tests;

use self::drag::discard_pending_drags_for;
use self::presentation::{
    emit_workspace_view_presentations_changed, seed_item_window_presentation,
};

const SESSION_LABEL_PREFIX: &str = "session:";
const FILE_LABEL_PREFIX: &str = "file:";
const CONTEXT_LABEL_PREFIX: &str = "context:";
const DEFAULT_WIDTH: f64 = 960.0;
const DEFAULT_HEIGHT: f64 = 720.0;
const MIN_WIDTH: f64 = 620.0;
const MIN_HEIGHT: f64 = 480.0;
/// Places the new window's tab band under the pointer; the OS may still move it.
const DROP_POINTER_X_INSET: f64 = 148.0;
const DROP_POINTER_Y_INSET: f64 = 18.0;

/// The main window is Window 1; peers count up from 2 and numbers are never reused.
const MAIN_VIEW_NUMBER: u64 = 1;
const FIRST_PEER_VIEW_NUMBER: u64 = MAIN_VIEW_NUMBER + 1;

static NEXT_VIEW_ID: AtomicU64 = AtomicU64::new(FIRST_PEER_VIEW_NUMBER);

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ItemWindowView {
    pub label: String,
    pub title: String,
    pub view_number: u64,
    pub kind: String,
    pub project_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub root_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stage_id: Option<String>,
}

static ITEM_WINDOW_VIEWS: LazyLock<Mutex<HashMap<String, ItemWindowView>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

#[derive(Clone, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkspaceContext {
    pub kind: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub project_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stage_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub panel_id: Option<String>,
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkspaceViewPresentation {
    pub label: String,
    pub view_id: String,
    pub contexts: Vec<WorkspaceContext>,
}

static WORKSPACE_VIEW_PRESENTATIONS: LazyLock<Mutex<HashMap<String, WorkspaceViewPresentation>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

struct PendingItemWindowDrag {
    initiator_label: String,
    view: ItemWindowView,
    hovered_target: Option<String>,
}

static PENDING_ITEM_WINDOW_DRAGS: LazyLock<Mutex<HashMap<String, PendingItemWindowDrag>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

#[derive(Clone)]
struct FileTabDropTarget {
    project_id: String,
    x: f64,
    y: f64,
    width: f64,
    height: f64,
}

static FILE_TAB_DROP_TARGETS: LazyLock<Mutex<HashMap<String, FileTabDropTarget>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

const FILE_TAB_DRAG_EVENT: &str = "file-tab-window-drag";

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct FileTabDragEvent {
    phase: String,
    drag_label: String,
    project_id: String,
    root_id: String,
    path: String,
    title: String,
    position_x: f64,
    position_y: f64,
}

fn valid_id(value: &str) -> bool {
    !value.is_empty() && value.chars().all(|c| c.is_ascii_alphanumeric() || c == '-')
}

fn percent_encode(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            out.push(byte as char);
        } else {
            out.push('%');
            out.push_str(&format!("{byte:02X}"));
        }
    }
    out
}

fn stable_file_id(project_id: &str, root_id: &str, path: &str) -> String {
    // FNV-1a gives the file a stable label component.
    let mut hash = 0xcbf29ce484222325_u64;
    for byte in format!("{project_id}\0{root_id}\0{path}").bytes() {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(0x100000001b3);
    }
    format!("{hash:016x}")
}

fn next_view_label(prefix: &str, identity: &str) -> (String, u64) {
    let view_id = NEXT_VIEW_ID.fetch_add(1, Ordering::Relaxed);
    (format!("{prefix}{identity}:{view_id}"), view_id)
}

fn item_window_views() -> Vec<ItemWindowView> {
    let Ok(views) = ITEM_WINDOW_VIEWS.lock() else {
        return Vec::new();
    };
    let mut out: Vec<_> = views.values().cloned().collect();
    out.sort_by_key(|view| view.view_number);
    out
}

fn emit_item_window_views_changed(app: &AppHandle) {
    let _ = app.emit("item-window-views-changed", item_window_views());
}

#[tauri::command(rename = "list_item_window_views")]
pub fn ipc_list_item_window_views() -> Vec<ItemWindowView> {
    item_window_views()
}

#[tauri::command(rename = "focus_item_window")]
pub fn ipc_focus_item_window(app: AppHandle, label: String) -> Result<bool, String> {
    let known = ITEM_WINDOW_VIEWS
        .lock()
        .map_err(|_| "item window registry lock poisoned".to_string())?
        .contains_key(&label);
    if !known && label != "main" {
        return Ok(false);
    }
    let Some(window) = app.get_webview_window(&label) else {
        return Ok(false);
    };
    // Closing the main window only hides it.
    let _ = window.show();
    let _ = window.unminimize();
    window.set_focus().map_err(|err| err.to_string())?;
    Ok(true)
}

#[tauri::command(rename = "close_item_window")]
pub fn ipc_close_item_window(app: AppHandle, label: String) -> Result<bool, String> {
    let known = ITEM_WINDOW_VIEWS
        .lock()
        .map_err(|_| "item window registry lock poisoned".to_string())?
        .contains_key(&label);
    if !known {
        return Ok(false);
    }
    let Some(window) = app.get_webview_window(&label) else {
        return Ok(false);
    };
    window.close().map_err(|err| err.to_string())?;
    Ok(true)
}

fn open_item_window(
    app: AppHandle,
    kind: String,
    project_id: String,
    session_id: Option<String>,
    root_id: Option<String>,
    path: Option<String>,
    stage_id: Option<String>,
    title: String,
    position_x: Option<f64>,
    position_y: Option<f64>,
    drag_initiator: Option<String>,
) -> Result<String, String> {
    let _creation = crate::app_exit::begin_window(&app)?;
    let is_drag = drag_initiator.is_some();
    if !valid_id(&project_id) {
        return Err("invalid item window project".into());
    }
    let (label, view_id, session_id, root_id, path, stage_id) = match kind.as_str() {
        "session" => {
            let session_id = session_id
                .filter(|value| valid_id(value))
                .ok_or("session id is required")?;
            let (label, view_id) = next_view_label(SESSION_LABEL_PREFIX, &session_id);
            (label, view_id, Some(session_id), None, None, None)
        }
        "file" => {
            let root_id = root_id
                .filter(|value| valid_id(value))
                .ok_or("file root is required")?;
            let path = path
                .filter(|value| !value.trim().is_empty())
                .ok_or("file path is required")?;
            let id = stable_file_id(&project_id, &root_id, &path);
            let (label, view_id) = next_view_label(FILE_LABEL_PREFIX, &id);
            (label, view_id, None, Some(root_id), Some(path), None)
        }
        "context" => {
            let stage_id = stage_id
                .filter(|value| valid_id(value))
                .ok_or("context stage is required")?;
            let (label, view_id) = next_view_label(CONTEXT_LABEL_PREFIX, &stage_id);
            (label, view_id, None, None, None, Some(stage_id))
        }
        _ => return Err("unknown item window kind".into()),
    };

    let mut query = format!(
        "window_subject={}&project_id={}&view_id={}",
        percent_encode(&kind),
        percent_encode(&project_id),
        view_id,
    );
    if let Some(session_id) = session_id.as_deref() {
        query.push_str("&session_id=");
        query.push_str(&percent_encode(session_id));
    }
    if let Some(path) = path.as_deref() {
        query.push_str("&root_id=");
        query.push_str(&percent_encode(root_id.as_deref().unwrap_or_default()));
        query.push_str("&path=");
        query.push_str(&percent_encode(path));
    }
    if let Some(stage_id) = stage_id.as_deref() {
        query.push_str("&stage_id=");
        query.push_str(&percent_encode(stage_id));
    }
    let window_title = format!("{title} — Window {view_id}");
    let mut builder = crate::webkit_features::with_shell_configuration(WebviewWindowBuilder::new(
        &app,
        &label,
        WebviewUrl::App(format!("index.html?{query}").into()),
    ))
    .title(window_title)
    .inner_size(DEFAULT_WIDTH, DEFAULT_HEIGHT)
    .min_inner_size(MIN_WIDTH, MIN_HEIGHT)
    .resizable(true)
    .visible(false)
    .background_color(crate::window_appearance::background_for_new_window(&app))
    .decorations(true);

    if is_drag {
        builder = builder
            .always_on_top(true)
            .focusable(false)
            .focused(false)
            .skip_taskbar(true);
    }

    if let (Some(x), Some(y)) = (position_x, position_y) {
        if x.is_finite() && y.is_finite() {
            builder = builder.position(x - DROP_POINTER_X_INSET, y - DROP_POINTER_Y_INSET);
        }
    }

    #[cfg(target_os = "macos")]
    {
        builder = builder
            .hidden_title(true)
            .title_bar_style(TitleBarStyle::Overlay)
            .traffic_light_position(Position::Logical(LogicalPosition::new(16.0, 16.0)));
    }

    let window = match builder.build() {
        Ok(window) => window,
        Err(err) => return Err(err.to_string()),
    };
    if is_drag {
        if let Err(err) = window.set_ignore_cursor_events(true) {
            let _ = window.close();
            return Err(err.to_string());
        }
    }
    crate::window_appearance::prepare(&window);
    let view = ItemWindowView {
        label: label.clone(),
        title,
        view_number: view_id,
        kind: kind.clone(),
        project_id: project_id.clone(),
        session_id: session_id.clone(),
        root_id: root_id.clone(),
        path: path.clone(),
        stage_id: stage_id.clone(),
    };
    if let Some(initiator_label) = drag_initiator {
        PENDING_ITEM_WINDOW_DRAGS
            .lock()
            .map_err(|_| "item window drag registry lock poisoned".to_string())?
            .insert(
                label.clone(),
                PendingItemWindowDrag {
                    initiator_label,
                    view,
                    hovered_target: None,
                },
            );
    } else {
        ITEM_WINDOW_VIEWS
            .lock()
            .map_err(|_| "item window registry lock poisoned".to_string())?
            .insert(label.clone(), view.clone());
        seed_item_window_presentation(&label, &view)?;
    }
    let app_on_destroy = app.clone();
    let label_on_destroy = label.clone();
    window.on_window_event(move |event| {
        if !matches!(event, WindowEvent::Destroyed) {
            return;
        }
        if let Ok(mut views) = ITEM_WINDOW_VIEWS.lock() {
            views.remove(&label_on_destroy);
        }
        if let Ok(mut presentations) = WORKSPACE_VIEW_PRESENTATIONS.lock() {
            presentations.remove(&label_on_destroy);
        }
        if let Ok(mut drags) = PENDING_ITEM_WINDOW_DRAGS.lock() {
            drags.remove(&label_on_destroy);
        }
        // Destroyed windows leave their pending drags for cleanup.
        let _ = discard_pending_drags_for(&app_on_destroy, &label_on_destroy);
        if let Ok(mut targets) = FILE_TAB_DROP_TARGETS.lock() {
            targets.remove(&label_on_destroy);
        }
        // Release the destroyed window's paint state.
        crate::window_backdrop::forget_painted(&label_on_destroy);
        crate::document_outbox::reconcile_windows(&app_on_destroy, Some(&label_on_destroy));
        emit_item_window_views_changed(&app_on_destroy);
        emit_workspace_view_presentations_changed(&app_on_destroy);
    });
    if !is_drag {
        emit_item_window_views_changed(&app);
        emit_workspace_view_presentations_changed(&app);
    }
    Ok(label)
}

#[tauri::command(rename = "open_item_window")]
pub fn ipc_open_item_window(
    app: AppHandle,
    kind: String,
    project_id: String,
    session_id: Option<String>,
    root_id: Option<String>,
    path: Option<String>,
    stage_id: Option<String>,
    title: String,
    position_x: Option<f64>,
    position_y: Option<f64>,
) -> Result<(), String> {
    open_item_window(
        app, kind, project_id, session_id, root_id, path, stage_id, title, position_x, position_y,
        None,
    )
    .map(drop)
}
