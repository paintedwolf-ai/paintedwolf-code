use super::daemon::attach_existing_daemon;
use super::diagnostics::export_startup_diagnostics;
use super::supervisor::EngineState;
use super::{
    cancel_sidecar_start, reset_credential_vault, restart_sidecar_with_progress,
    start_sidecar_with_progress, SidecarInfo, SidecarStartError, SidecarState,
};
use tauri::{Emitter, Manager};

#[tauri::command(rename = "start_sidecar")]
pub async fn ipc_start_sidecar(
    app: tauri::AppHandle,
    password: Option<String>,
) -> Result<SidecarInfo, SidecarStartError> {
    app.state::<crate::update_service::UpdateService>()
        .wait_for_startup()
        .await;
    let handle = app.clone();
    let info = tauri::async_runtime::spawn_blocking(move || {
        let state = handle.state::<SidecarState>();
        let progress_handle = handle.clone();
        start_sidecar_with_progress(&state, password, move |progress| {
            let _ = progress_handle.emit("sidecar-startup", &progress);
        })
    })
    .await
    .map_err(|e| SidecarStartError::Failed(format!("start task join: {e}")))??;
    if let Err(error) = crate::update_service::transaction::confirm_startup(&app, &info).await {
        eprintln!("Update startup receipt: {error}");
    }
    Ok(info)
}

#[tauri::command(rename = "export_startup_diagnostics")]
pub async fn ipc_export_startup_diagnostics() -> Result<Vec<u8>, String> {
    tauri::async_runtime::spawn_blocking(export_startup_diagnostics)
        .await
        .map_err(|e| format!("diagnostics task join: {e}"))?
}

#[tauri::command(rename = "restart_sidecar")]
pub async fn ipc_restart_sidecar(app: tauri::AppHandle) -> Result<SidecarInfo, SidecarStartError> {
    app.state::<crate::update_service::UpdateService>()
        .wait_for_startup()
        .await;
    let handle = app.clone();
    let info = tauri::async_runtime::spawn_blocking(move || {
        let state = handle.state::<SidecarState>();
        let progress_handle = handle.clone();
        restart_sidecar_with_progress(&state, move |progress| {
            let _ = progress_handle.emit("sidecar-startup", &progress);
        })
    })
    .await
    .map_err(|e| SidecarStartError::Failed(format!("restart task join: {e}")))??;
    if let Err(error) = crate::update_service::transaction::confirm_startup(&app, &info).await {
        eprintln!("Update startup receipt: {error}");
    }
    Ok(info)
}

#[tauri::command(rename = "reset_credential_vault")]
pub async fn ipc_reset_credential_vault(app: tauri::AppHandle) -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SidecarState>();
        reset_credential_vault(&state)
    })
    .await
    .map_err(|err| format!("credential vault reset task join: {err}"))?
}

#[tauri::command(rename = "cancel_sidecar_start")]
pub fn ipc_cancel_sidecar_start(state: tauri::State<'_, SidecarState>) -> bool {
    cancel_sidecar_start(&state)
}

#[tauri::command(rename = "attach_existing_daemon")]
pub async fn ipc_attach_existing_daemon(
    app: tauri::AppHandle,
) -> Result<Option<SidecarInfo>, String> {
    app.state::<crate::update_service::UpdateService>()
        .wait_for_startup()
        .await;
    tauri::async_runtime::spawn_blocking(|| attach_existing_daemon())
        .await
        .map_err(|e| format!("attach task join: {e}"))?
}

#[tauri::command(rename = "sidecar_info")]
pub async fn ipc_sidecar_info(
    app: tauri::AppHandle,
    state: tauri::State<'_, SidecarState>,
) -> Result<Option<SidecarInfo>, String> {
    app.state::<crate::update_service::UpdateService>()
        .wait_for_startup()
        .await;
    if let Some(info) = state.cached_info() {
        return Ok(Some(info));
    }
    tauri::async_runtime::spawn_blocking(|| attach_existing_daemon().ok().flatten())
        .await
        .map_err(|e| e.to_string())
}

/// The engine this shell runs, for windows that open after a change was published.
#[tauri::command(rename = "engine_state")]
pub fn ipc_engine_state(state: tauri::State<'_, SidecarState>) -> EngineState {
    state.engine_state()
}
