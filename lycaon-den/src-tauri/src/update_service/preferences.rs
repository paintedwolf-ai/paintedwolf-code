use super::{
    emit, persistence, Discovery, Failure, Installation, NativeUpdateState, UpdateChannel,
    UpdateError, UpdateService,
};
use tauri::{AppHandle, Manager};
#[tauri::command]
pub async fn get_update_state(app: AppHandle) -> NativeUpdateState {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    inner
        .state
        .refresh_capabilities(super::installer::supported());
    inner.state.clone()
}
async fn set(
    app: &AppHandle,
    enabled: Option<bool>,
    channel: Option<UpdateChannel>,
) -> Result<NativeUpdateState, UpdateError> {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    if matches!(
        inner.state.installation,
        Installation::AwaitingExit | Installation::Activating
    ) {
        return Err(Failure::InvalidTransition.into());
    }
    if !inner.preferences_writable {
        return Err(Failure::PreferencesUnavailable.into());
    }
    let mut preferences = inner.preferences.clone();
    if let Some(value) = enabled {
        preferences.automatic_updates_enabled = value;
    }
    if let Some(value) = channel {
        preferences.channel = value;
    }
    persistence::write_preferences(&persistence::update_dir()?, &preferences)?;
    service.cancel();
    inner.state.automatic_updates_enabled = preferences.automatic_updates_enabled;
    let mut cleanup_error = None;
    if inner.state.channel != preferences.channel {
        inner.state.candidate = None;
        inner.state.staged_release_id = None;
        inner.state.installation = Installation::None;
        cleanup_error = super::staging::forget_ready().err();
    } else if inner.state.installation.busy() {
        inner.state.installation = Installation::None;
    }
    inner.state.channel = preferences.channel;
    inner.state.discovery = Discovery::Idle;
    inner.state.last_error = cleanup_error;
    inner.preferences = preferences;
    emit(app, &mut inner.state);
    Ok(inner.state.clone())
}
#[tauri::command]
pub async fn set_automatic_updates_enabled(
    app: AppHandle,
    enabled: bool,
) -> Result<NativeUpdateState, UpdateError> {
    set(&app, Some(enabled), None).await
}
#[tauri::command]
pub async fn set_update_channel(
    app: AppHandle,
    channel: UpdateChannel,
) -> Result<NativeUpdateState, UpdateError> {
    set(&app, None, Some(channel)).await
}
