//! Plain-text clipboard access for copy, cut, and paste actions.
//!
//! Every window shares one serialized service over the platform adapter:
//! GTK on Linux, the OS pasteboard elsewhere. Shutdown is terminal.

mod service;

#[cfg(target_os = "linux")]
mod linux;
#[cfg(target_os = "linux")]
use linux::{startup_facts, NativeClipboard};
#[cfg(not(target_os = "linux"))]
mod system;
#[cfg(not(target_os = "linux"))]
use system::{startup_facts, NativeClipboard};

pub use service::{ClipboardError, ClipboardFacts};
use service::ClipboardService;
use std::sync::OnceLock;
use tauri::Manager;

#[derive(Default)]
pub struct ClipboardState {
    service: ClipboardService<NativeClipboard>,
    facts: OnceLock<ClipboardFacts>,
}

impl ClipboardState {
    pub fn shutdown(&self, app: &tauri::AppHandle) {
        self.service.shutdown(app);
    }
}

/// Records the display facts once the toolkit is ready; runs on the main thread.
pub fn setup(app: &tauri::AppHandle) {
    match startup_facts() {
        Ok(facts) => {
            eprintln!(
                "Clipboard: protocol={} persistence={}",
                serde_json::to_value(facts.protocol)
                    .ok()
                    .and_then(|value| value.as_str().map(str::to_owned))
                    .unwrap_or_default(),
                facts.persistence
            );
            let _ = app.state::<ClipboardState>().facts.set(facts);
        }
        Err(error) => eprintln!("Clipboard: {error}"),
    }
}

#[tauri::command]
pub async fn write_clipboard_text(
    text: String,
    app: tauri::AppHandle,
    state: tauri::State<'_, ClipboardState>,
) -> Result<(), ClipboardError> {
    state.service.write(&app, text).await
}

/// Empty when the board has no text.
#[tauri::command]
pub async fn read_clipboard_text(
    app: tauri::AppHandle,
    state: tauri::State<'_, ClipboardState>,
) -> Result<String, ClipboardError> {
    state.service.read(&app).await
}

/// The display protocol and capabilities the clipboard commands run against.
#[tauri::command]
pub async fn clipboard_status(
    app: tauri::AppHandle,
    state: tauri::State<'_, ClipboardState>,
) -> Result<ClipboardFacts, ClipboardError> {
    if let Some(facts) = state.facts.get() {
        return Ok(facts.clone());
    }
    state.service.facts(&app).await
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

#[cfg(all(test, target_os = "linux"))]
mod tests_linux;
