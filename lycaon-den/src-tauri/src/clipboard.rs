//! Plain-text clipboard access for copy, cut, and paste actions.

#[derive(Default)]
pub struct ClipboardState(std::sync::Mutex<Option<arboard::Clipboard>>);

impl ClipboardState {
    fn with_clipboard<T>(
        &self,
        action: impl FnOnce(&mut arboard::Clipboard) -> Result<T, String>,
    ) -> Result<T, String> {
        let mut owner = self
            .0
            .lock()
            .map_err(|_| "Clipboard state is unavailable")?;
        if owner.is_none() {
            *owner = Some(arboard::Clipboard::new().map_err(|e| e.to_string())?);
        }
        action(owner.as_mut().expect("clipboard initialized"))
    }

    pub fn shutdown(&self) {
        let mut owner = self
            .0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        owner.take();
    }
}

// Linux serves selection contents for the lifetime of the native owner.
#[tauri::command]
pub fn write_clipboard_text(
    text: String,
    state: tauri::State<'_, ClipboardState>,
) -> Result<(), String> {
    state.with_clipboard(|clipboard| clipboard.set_text(text).map_err(|e| e.to_string()))
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
pub fn read_clipboard_text(state: tauri::State<'_, ClipboardState>) -> Result<String, String> {
    state.with_clipboard(|clipboard| match clipboard.get_text() {
        Ok(text) => Ok(text),
        Err(arboard::Error::ContentNotAvailable) => Ok(String::new()),
        Err(e) => Err(e.to_string()),
    })
}

#[cfg(all(test, target_os = "linux"))]
mod tests_linux;
