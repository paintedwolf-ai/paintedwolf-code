//! Desktop notification delivery, activation, and badges.

#[cfg(target_os = "macos")]
pub(crate) mod native;

#[cfg(target_os = "linux")]
mod linux;

use tauri::{AppHandle, Manager};

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
pub(crate) async fn den_notify_permission_state(app: AppHandle) -> Result<String, String> {
    #[cfg(not(target_os = "linux"))]
    let _ = app;
    #[cfg(target_os = "macos")]
    {
        await_un(|| native::authorization_state().map(|s| s.as_str().to_string())).await
    }
    #[cfg(target_os = "linux")]
    {
        linux::permission(&app).await
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        Ok("unavailable".to_string())
    }
}

#[tauri::command]
pub(crate) async fn den_notify_request_permission(app: AppHandle) -> Result<String, String> {
    #[cfg(not(target_os = "linux"))]
    let _ = app;
    #[cfg(target_os = "macos")]
    {
        await_un(|| native::request_authorization().map(|s| s.as_str().to_string())).await
    }
    #[cfg(target_os = "linux")]
    {
        linux::permission(&app).await
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        Ok("unavailable".to_string())
    }
}

/// Resolves to `delivered`, or `inhibited` when the service accepted the
/// notification while withholding banners.
#[tauri::command]
pub(crate) async fn den_notify_send(
    app: AppHandle,
    title: String,
    body: String,
    session_id: Option<String>,
) -> Result<String, String> {
    #[cfg(not(target_os = "linux"))]
    let _ = app;
    #[cfg(target_os = "macos")]
    {
        await_un(move || native::send(&title, &body, session_id.as_deref()))
            .await
            .map(|()| "delivered".to_string())
    }
    #[cfg(target_os = "linux")]
    {
        linux::send(&app, title, body, session_id).await
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        let _ = (title, body, session_id);
        Err("Native desktop notifications are unavailable".into())
    }
}

/// Nonpositive counts clear the dock badge.
#[tauri::command]
pub(crate) fn den_set_badge_count(app: tauri::AppHandle, count: i64) -> Result<(), String> {
    let Some(window) = app.get_webview_window("main") else {
        return Err("Main window is unavailable".into());
    };
    let value = if count > 0 { Some(count) } else { None };
    window.set_badge_count(value).map_err(|e| e.to_string())
}

#[tauri::command]
pub(crate) async fn den_notify_cancel_sessions(
    app: AppHandle,
    session_ids: Vec<String>,
) -> Result<(), String> {
    #[cfg(not(target_os = "linux"))]
    let _ = app;
    #[cfg(target_os = "macos")]
    {
        await_un(move || native::cancel_sessions(&session_ids)).await
    }
    #[cfg(target_os = "linux")]
    {
        linux::cancel(&app, session_ids).await
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        let _ = session_ids;
        Err("Native desktop notifications are unavailable".into())
    }
}

pub(crate) fn setup(app: AppHandle) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        native::setup(app)
    }
    #[cfg(target_os = "linux")]
    {
        linux::setup(app)
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        let _ = app;
        Ok(())
    }
}

pub(crate) fn shutdown(app: &AppHandle) {
    #[cfg(target_os = "linux")]
    linux::shutdown(app);
    #[cfg(not(target_os = "linux"))]
    let _ = app;
}
