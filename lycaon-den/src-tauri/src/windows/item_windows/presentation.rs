use super::{
    valid_id, ItemWindowView, WorkspaceContext, WorkspaceViewPresentation,
    WORKSPACE_VIEW_PRESENTATIONS,
};
use tauri::{AppHandle, Emitter, WebviewWindow};

pub(super) fn valid_workspace_context(context: &WorkspaceContext) -> bool {
    let project_valid = context.project_id.as_deref().is_some_and(valid_id);
    match context.kind.as_str() {
        "session" => {
            project_valid
                && context.session_id.as_deref().is_some_and(valid_id)
                && context.stage_id.is_none()
                && context.panel_id.is_none()
        }
        "stage" => {
            context.project_id.as_deref().is_none_or(valid_id)
                && context.stage_id.as_deref().is_some_and(valid_id)
                && context.session_id.is_none()
                && context.panel_id.is_none()
        }
        "panel" => {
            context.panel_id.as_deref().is_some_and(valid_id)
                && context.session_id.is_none()
                && context.stage_id.is_none()
                && context.project_id.as_deref().is_none_or(valid_id)
        }
        _ => false,
    }
}

fn workspace_view_presentations() -> Vec<WorkspaceViewPresentation> {
    let Ok(presentations) = WORKSPACE_VIEW_PRESENTATIONS.lock() else {
        return Vec::new();
    };
    let mut out: Vec<_> = presentations.values().cloned().collect();
    out.sort_by(|left, right| left.label.cmp(&right.label));
    out
}

pub(super) fn seed_item_window_presentation(
    label: &str,
    view: &ItemWindowView,
) -> Result<(), String> {
    let context = match view.kind.as_str() {
        "session" => WorkspaceContext {
            kind: "session".into(),
            project_id: Some(view.project_id.clone()),
            session_id: view.session_id.clone(),
            stage_id: None,
            panel_id: None,
        },
        "file" => WorkspaceContext {
            kind: "stage".into(),
            project_id: Some(view.project_id.clone()),
            session_id: None,
            stage_id: Some("files".into()),
            panel_id: None,
        },
        "context" => WorkspaceContext {
            kind: "stage".into(),
            project_id: Some(view.project_id.clone()),
            session_id: None,
            stage_id: view.stage_id.clone(),
            panel_id: None,
        },
        _ => return Err("invalid item window presentation".into()),
    };
    WORKSPACE_VIEW_PRESENTATIONS
        .lock()
        .map_err(|_| "workspace view registry lock poisoned".to_string())?
        .insert(
            label.to_string(),
            WorkspaceViewPresentation {
                label: label.to_string(),
                view_id: view.view_number.to_string(),
                contexts: vec![context],
            },
        );
    Ok(())
}

pub(super) fn emit_workspace_view_presentations_changed(app: &AppHandle) {
    let _ = app.emit(
        "workspace-view-presentations-changed",
        workspace_view_presentations(),
    );
}

#[tauri::command(rename = "list_workspace_view_presentations")]
pub fn ipc_list_workspace_view_presentations() -> Vec<WorkspaceViewPresentation> {
    workspace_view_presentations()
}

#[tauri::command(rename = "set_workspace_view_contexts")]
pub fn ipc_set_workspace_view_contexts(
    app: AppHandle,
    window: WebviewWindow,
    view_id: String,
    contexts: Vec<WorkspaceContext>,
) -> Result<(), String> {
    if !valid_id(&view_id) || !contexts.iter().all(valid_workspace_context) {
        return Err("invalid workspace view presentation".into());
    }
    let mut deduped = Vec::with_capacity(contexts.len());
    for context in contexts {
        if !deduped.contains(&context) {
            deduped.push(context);
        }
    }
    WORKSPACE_VIEW_PRESENTATIONS
        .lock()
        .map_err(|_| "workspace view registry lock poisoned".to_string())?
        .insert(
            window.label().to_string(),
            WorkspaceViewPresentation {
                label: window.label().to_string(),
                view_id,
                contexts: deduped,
            },
        );
    emit_workspace_view_presentations_changed(&app);
    Ok(())
}
