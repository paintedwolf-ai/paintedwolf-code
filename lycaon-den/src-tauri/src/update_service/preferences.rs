use super::persistence::write_preferences;
use super::check::run_automatic_check;
use super::{
    emit_update_state, Failure, NativeUpdateState, UpdateChannel, UpdateError, UpdatePreferences,
    UpdateService,
};
use tauri::AppHandle;

#[tauri::command]
pub async fn get_update_state(
    app: AppHandle,
    service: tauri::State<'_, UpdateService>,
) -> Result<NativeUpdateState, UpdateError> {
    let mut inner = service.inner.lock().await;
    let current = inner.state.current_version.clone();
    if let Some(dir) = crate::den_state_dir() {
        inner.reconcile_state(&dir, &current);
    }
    emit_update_state(&app, &mut inner.state);
    let state = inner.state.clone();
    drop(inner);
    Ok(state)
}

#[tauri::command]
pub async fn set_update_checks_enabled(
    app: AppHandle,
    service: tauri::State<'_, UpdateService>,
    enabled: bool,
) -> Result<NativeUpdateState, UpdateError> {
    let _operation = service.operation.lock().await;
    let mut inner = service.inner.lock().await;
    let dir = crate::den_state_dir().ok_or_else(|| UpdateError::from(Failure::StateUnavailable))?;
    write_preferences(
        &dir,
        &UpdatePreferences {
            checks_enabled: enabled,
            channel: inner.state.channel,
            rollout_bucket: inner.rollout_bucket,
        },
    )?;
    inner.set_checks_enabled(enabled);
    emit_update_state(&app, &mut inner.state);
    let state = inner.state.clone();
    drop(inner);
    if enabled {
        tauri::async_runtime::spawn(async move {
            let _ = run_automatic_check(app).await;
        });
    }
    Ok(state)
}

#[tauri::command]
pub async fn set_update_channel(
    app: AppHandle,
    service: tauri::State<'_, UpdateService>,
    channel: UpdateChannel,
) -> Result<NativeUpdateState, UpdateError> {
    let _operation = service.operation.lock().await;
    let mut inner = service.inner.lock().await;
    let dir = crate::den_state_dir().ok_or_else(|| UpdateError::from(Failure::StateUnavailable))?;
    write_preferences(
        &dir,
        &UpdatePreferences {
            checks_enabled: inner.state.checks_enabled,
            channel,
            rollout_bucket: inner.rollout_bucket,
        },
    )?;
    inner.set_channel(channel);
    emit_update_state(&app, &mut inner.state);
    let state = inner.state.clone();
    let should_check = state.checks_enabled;
    drop(inner);
    if should_check {
        tauri::async_runtime::spawn(async move {
            let _ = run_automatic_check(app).await;
        });
    }
    Ok(state)
}
