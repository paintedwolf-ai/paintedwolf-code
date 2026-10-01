//! Plain-text clipboard access for copy, cut, and paste actions.

/// Writes plain text to the OS clipboard.
#[tauri::command]
pub fn write_clipboard_text(text: String) -> Result<(), String> {
    let mut clipboard = arboard::Clipboard::new().map_err(|e| e.to_string())?;
    clipboard.set_text(text).map_err(|e| e.to_string())
}

/// Pastes into the focused web view through the system paste action, the
/// same path Edit > Paste takes.
#[tauri::command]
pub fn paste_into_focused_view(app: tauri::AppHandle) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        app.run_on_main_thread(|| {
            let Some(mtm) = objc2::MainThreadMarker::new() else {
                return;
            };
            let ns_app = objc2_app_kit::NSApplication::sharedApplication(mtm);
            unsafe {
                ns_app.sendAction_to_from(objc2::sel!(paste:), None, None);
            }
        })
        .map_err(|e| e.to_string())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        Err("system paste is available on macOS only".to_string())
    }
}

/// Empty when the board has no text.
#[tauri::command]
pub fn read_clipboard_text() -> Result<String, String> {
    let mut clipboard = arboard::Clipboard::new().map_err(|e| e.to_string())?;
    match clipboard.get_text() {
        Ok(text) => Ok(text),
        Err(arboard::Error::ContentNotAvailable) => Ok(String::new()),
        Err(e) => Err(e.to_string()),
    }
}
