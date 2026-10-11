use super::presentation::{
    emit_workspace_view_presentations_changed, seed_item_window_presentation,
};
use super::{
    emit_item_window_views_changed, open_item_window, valid_id, FileTabDragEvent,
    FileTabDropTarget, ItemWindowView, DROP_POINTER_X_INSET, DROP_POINTER_Y_INSET,
    FILE_TAB_DRAG_EVENT, FILE_TAB_DROP_TARGETS, ITEM_WINDOW_VIEWS, PENDING_ITEM_WINDOW_DRAGS,
};
use tauri::{AppHandle, Emitter, LogicalPosition, Manager, WebviewWindow};

#[tauri::command(rename = "begin_file_item_window_drag")]
pub fn ipc_begin_file_item_window_drag(
    app: AppHandle,
    window: WebviewWindow,
    project_id: String,
    root_id: String,
    path: String,
    title: String,
    position_x: f64,
    position_y: f64,
) -> Result<String, String> {
    open_item_window(
        app,
        "file".into(),
        project_id,
        None,
        Some(root_id),
        Some(path),
        None,
        title,
        Some(position_x),
        Some(position_y),
        Some(window.label().to_string()),
    )
}

/// Takes every drag this initiator left pending out of the registry.
pub(super) fn take_pending_drags_for(
    initiator_label: &str,
) -> Result<Vec<(String, ItemWindowView, Option<String>)>, String> {
    let mut drags = PENDING_ITEM_WINDOW_DRAGS
        .lock()
        .map_err(|_| "item window drag registry lock poisoned".to_string())?;
    let labels: Vec<String> = drags
        .iter()
        .filter(|(_, drag)| drag.initiator_label == initiator_label)
        .map(|(label, _)| label.clone())
        .collect();
    Ok(labels
        .into_iter()
        .filter_map(|label| {
            drags
                .remove(&label)
                .map(|drag| (label, drag.view, drag.hovered_target))
        })
        .collect())
}

/// Hidden pending windows require explicit cleanup.
pub(super) fn discard_pending_drags_for(
    app: &AppHandle,
    initiator_label: &str,
) -> Result<Vec<String>, String> {
    let pending = take_pending_drags_for(initiator_label)?;
    let mut discarded = Vec::with_capacity(pending.len());
    for (label, view, hovered_target) in pending {
        if let Some(target) = hovered_target {
            let _ = emit_file_tab_drag_event(app, &target, "leave", &label, &view, 0.0, 0.0);
        }
        if let Some(drag_window) = app.get_webview_window(&label) {
            let _ = drag_window.close();
        }
        discarded.push(label);
    }
    Ok(discarded)
}

#[tauri::command(rename = "discard_pending_item_window_drags")]
pub fn ipc_discard_pending_item_window_drags(
    app: AppHandle,
    window: WebviewWindow,
) -> Result<Vec<String>, String> {
    discard_pending_drags_for(&app, window.label())
}

fn pending_drag_view(
    initiator: &WebviewWindow,
    label: &str,
) -> Result<Option<ItemWindowView>, String> {
    let drags = PENDING_ITEM_WINDOW_DRAGS
        .lock()
        .map_err(|_| "item window drag registry lock poisoned".to_string())?;
    Ok(drags
        .get(label)
        .and_then(|drag| (drag.initiator_label == initiator.label()).then(|| drag.view.clone())))
}

#[tauri::command(rename = "update_file_tab_drop_target")]
pub fn ipc_update_file_tab_drop_target(
    window: WebviewWindow,
    project_id: String,
    x: f64,
    y: f64,
    width: f64,
    height: f64,
) -> Result<(), String> {
    if !valid_id(&project_id)
        || ![x, y, width, height].into_iter().all(f64::is_finite)
        || width <= 0.0
        || height <= 0.0
    {
        return Err("invalid file tab drop target".into());
    }
    FILE_TAB_DROP_TARGETS
        .lock()
        .map_err(|_| "file tab drop target registry lock poisoned".to_string())?
        .insert(
            window.label().to_string(),
            FileTabDropTarget {
                project_id,
                x,
                y,
                width,
                height,
            },
        );
    Ok(())
}

#[tauri::command(rename = "clear_file_tab_drop_target")]
pub fn ipc_clear_file_tab_drop_target(window: WebviewWindow) -> Result<(), String> {
    FILE_TAB_DROP_TARGETS
        .lock()
        .map_err(|_| "file tab drop target registry lock poisoned".to_string())?
        .remove(window.label());
    Ok(())
}

fn file_tab_drop_target_at(
    app: &AppHandle,
    initiator_label: &str,
    drag_label: &str,
    project_id: &str,
    x: f64,
    y: f64,
) -> Option<String> {
    let targets = FILE_TAB_DROP_TARGETS.lock().ok()?.clone();
    targets.into_iter().find_map(|(label, target)| {
        if label == initiator_label || label == drag_label || target.project_id != project_id {
            return None;
        }
        let target_window = app.get_webview_window(&label)?;
        let scale = target_window.scale_factor().ok()?;
        let origin = target_window
            .inner_position()
            .ok()?
            .to_logical::<f64>(scale);
        let left = origin.x + target.x;
        let top = origin.y + target.y;
        (x >= left && x <= left + target.width && y >= top && y <= top + target.height)
            .then_some(label)
    })
}

fn emit_file_tab_drag_event(
    app: &AppHandle,
    target_label: &str,
    phase: &str,
    drag_label: &str,
    view: &ItemWindowView,
    position_x: f64,
    position_y: f64,
) -> Result<(), String> {
    app.emit_to(
        target_label,
        FILE_TAB_DRAG_EVENT,
        FileTabDragEvent {
            phase: phase.into(),
            drag_label: drag_label.into(),
            project_id: view.project_id.clone(),
            root_id: view.root_id.clone().unwrap_or_default(),
            path: view.path.clone().unwrap_or_default(),
            title: view.title.clone(),
            position_x,
            position_y,
        },
    )
    .map_err(|err| err.to_string())
}

fn set_pending_drag_hover(
    initiator: &WebviewWindow,
    label: &str,
    target: Option<String>,
) -> Result<Option<String>, String> {
    let mut drags = PENDING_ITEM_WINDOW_DRAGS
        .lock()
        .map_err(|_| "item window drag registry lock poisoned".to_string())?;
    let Some(drag) = drags.get_mut(label) else {
        return Ok(None);
    };
    if drag.initiator_label != initiator.label() {
        return Ok(None);
    }
    Ok(std::mem::replace(&mut drag.hovered_target, target))
}

fn set_drag_window_position(window: &WebviewWindow, x: f64, y: f64) -> Result<(), String> {
    if !x.is_finite() || !y.is_finite() {
        return Err("invalid item window drag position".into());
    }
    window
        .set_position(LogicalPosition::new(
            x - DROP_POINTER_X_INSET,
            y - DROP_POINTER_Y_INSET,
        ))
        .map_err(|err| err.to_string())
}

#[tauri::command(rename = "move_item_window_drag")]
pub fn ipc_move_item_window_drag(
    app: AppHandle,
    window: WebviewWindow,
    label: String,
    position_x: f64,
    position_y: f64,
) -> Result<bool, String> {
    let Some(view) = pending_drag_view(&window, &label)? else {
        return Ok(false);
    };
    let Some(drag_window) = app.get_webview_window(&label) else {
        return Ok(false);
    };
    set_drag_window_position(&drag_window, position_x, position_y)?;
    let target = file_tab_drop_target_at(
        &app,
        window.label(),
        &label,
        &view.project_id,
        position_x,
        position_y,
    );
    let previous = set_pending_drag_hover(&window, &label, target.clone())?;
    if previous != target {
        if let Some(previous) = previous {
            let _ = emit_file_tab_drag_event(
                &app, &previous, "leave", &label, &view, position_x, position_y,
            );
        }
    }
    if let Some(target) = target {
        let _ = drag_window.hide();
        let _ = emit_file_tab_drag_event(
            &app, &target, "hover", &label, &view, position_x, position_y,
        );
    } else {
        let _ = drag_window.show();
    }
    Ok(true)
}

#[tauri::command(rename = "finish_item_window_drag")]
pub fn ipc_finish_item_window_drag(
    app: AppHandle,
    window: WebviewWindow,
    label: String,
    position_x: f64,
    position_y: f64,
) -> Result<bool, String> {
    let Some(view) = pending_drag_view(&window, &label)? else {
        return Ok(false);
    };
    let Some(drag_window) = app.get_webview_window(&label) else {
        return Ok(false);
    };
    set_drag_window_position(&drag_window, position_x, position_y)?;
    let target = file_tab_drop_target_at(
        &app,
        window.label(),
        &label,
        &view.project_id,
        position_x,
        position_y,
    );
    let previous = set_pending_drag_hover(&window, &label, target.clone())?;
    if previous != target {
        if let Some(previous) = previous {
            let _ = emit_file_tab_drag_event(
                &app, &previous, "leave", &label, &view, position_x, position_y,
            );
        }
    }
    if let Some(target) = target {
        if emit_file_tab_drag_event(&app, &target, "drop", &label, &view, position_x, position_y)
            .is_ok()
        {
            PENDING_ITEM_WINDOW_DRAGS
                .lock()
                .map_err(|_| "item window drag registry lock poisoned".to_string())?
                .remove(&label);
            drag_window.close().map_err(|err| err.to_string())?;
            if let Some(target_window) = app.get_webview_window(&target) {
                let _ = target_window.set_focus();
            }
            return Ok(true);
        }
        let _ = emit_file_tab_drag_event(
            &app, &target, "leave", &label, &view, position_x, position_y,
        );
    }
    drag_window
        .set_ignore_cursor_events(false)
        .map_err(|err| err.to_string())?;
    drag_window
        .set_focusable(true)
        .map_err(|err| err.to_string())?;
    drag_window
        .set_always_on_top(false)
        .map_err(|err| err.to_string())?;
    drag_window
        .set_skip_taskbar(false)
        .map_err(|err| err.to_string())?;
    PENDING_ITEM_WINDOW_DRAGS
        .lock()
        .map_err(|_| "item window drag registry lock poisoned".to_string())?
        .remove(&label);
    ITEM_WINDOW_VIEWS
        .lock()
        .map_err(|_| "item window registry lock poisoned".to_string())?
        .insert(label.clone(), view.clone());
    seed_item_window_presentation(&label, &view)?;
    emit_item_window_views_changed(&app);
    emit_workspace_view_presentations_changed(&app);
    let _ = drag_window.show();
    let _ = drag_window.set_focus();
    Ok(true)
}

#[tauri::command(rename = "cancel_item_window_drag")]
pub fn ipc_cancel_item_window_drag(
    app: AppHandle,
    window: WebviewWindow,
    label: String,
) -> Result<bool, String> {
    let Some(view) = pending_drag_view(&window, &label)? else {
        return Ok(false);
    };
    let pending = PENDING_ITEM_WINDOW_DRAGS
        .lock()
        .map_err(|_| "item window drag registry lock poisoned".to_string())?
        .remove(&label);
    if let Some(target) = pending.and_then(|drag| drag.hovered_target) {
        let _ = emit_file_tab_drag_event(&app, &target, "leave", &label, &view, 0.0, 0.0);
    }
    let Some(drag_window) = app.get_webview_window(&label) else {
        return Ok(false);
    };
    drag_window.close().map_err(|err| err.to_string())?;
    Ok(true)
}
