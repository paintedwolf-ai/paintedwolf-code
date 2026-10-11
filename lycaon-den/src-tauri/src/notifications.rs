//! Desktop notification permissions, delivery, and dock badges.

#[cfg(target_os = "macos")]
pub(crate) mod native;

use tauri::Manager;

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
pub(crate) async fn den_notify_permission_state() -> Result<String, String> {
    #[cfg(target_os = "macos")]
    {
        await_un(|| native::authorization_state().map(|s| s.as_str().to_string()))
            .await
    }
    #[cfg(not(target_os = "macos"))]
    {
        Ok("denied".to_string())
    }
}

#[tauri::command]
pub(crate) async fn den_notify_request_permission() -> Result<String, String> {
    #[cfg(target_os = "macos")]
    {
        await_un(|| native::request_authorization().map(|s| s.as_str().to_string()))
            .await
    }
    #[cfg(not(target_os = "macos"))]
    {
        Ok("denied".to_string())
    }
}

#[tauri::command]
pub(crate) async fn den_notify_send(
    title: String,
    body: String,
    session_id: Option<String>,
) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        await_un(move || native::send(&title, &body, session_id.as_deref())).await
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (title, body, session_id);
        Ok(())
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
pub(crate) async fn den_notify_cancel_sessions(session_ids: Vec<String>) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        await_un(move || native::cancel_sessions(&session_ids)).await
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = session_ids;
        Ok(())
    }
}

