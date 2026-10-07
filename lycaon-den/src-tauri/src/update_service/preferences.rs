//! The device preference: automatic updating and the release channel.
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
    if inner.state.installation == Installation::RecoveryRequired {
        return Err(Failure::RecoveryRequired.into());
    }
    if inner.state.installation == Installation::AwaitingExit {
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
    persistence::write_preferences(&persistence::preferences_dir()?, &preferences)?;
    let changed_channel = inner.state.channel != preferences.channel;
    // A committed handoff belongs to the installation, not to the channel preference.
    let committed = inner.state.installation == Installation::Committed;
    if changed_channel {
        service.cancel(&mut inner.state);
        service
            .preparation_generation
            .fetch_add(1, std::sync::atomic::Ordering::AcqRel);
    } else {
        service.wake_scheduler();
    }
    if !preferences.automatic_updates_enabled
        && !inner.manual_preparation
        && inner.state.installation.busy()
        && !committed
    {
        service
            .preparation_generation
            .fetch_add(1, std::sync::atomic::Ordering::AcqRel);
        service.wake_scheduler();
        inner.state.installation = Installation::None;
    }
    inner.state.automatic_updates_enabled = preferences.automatic_updates_enabled;
    if changed_channel {
        inner.state.discovery = Discovery::Idle;
        if !committed {
            inner.state.candidate = None;
            inner.state.staged_release_id = None;
            inner.state.installation = Installation::None;
            inner.state.offer_confirmed_at = None;
            inner.state.last_error = super::staging::forget_ready().err();
        }
    }
    inner.state.channel = preferences.channel;
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
