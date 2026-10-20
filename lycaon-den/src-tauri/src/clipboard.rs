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

pub use service::ClipboardError;
use service::ClipboardService;

#[derive(Default)]
pub struct ClipboardState {
    service: ClipboardService<NativeClipboard>,
}

impl ClipboardState {
    pub fn shutdown(&self, app: &tauri::AppHandle) {
        self.service.shutdown(app);
    }
}

/// Logs the display facts once the toolkit is ready; runs on the main thread.
pub fn setup() {
    match startup_facts() {
        Ok(facts) => eprintln!(
            "Clipboard: protocol={} persistence={}",
            facts.protocol.as_str(),
            facts.persistence
        ),
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

/// Pastes into the focused web view through the system paste action, the
/// same path Edit > Paste takes.
#[tauri::command]
pub async fn paste_into_focused_view(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        let _ = window;
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
    #[cfg(target_os = "linux")]
    {
        use gtk::prelude::WidgetExt;
        use webkit2gtk::WebViewExt;
        let _ = app;
        let (reply, receiver) = tokio::sync::oneshot::channel();
        window
            .with_webview(move |view| {
                let view = view.inner();
                let result = if view.has_focus() {
                    view.execute_editing_command("Paste");
                    Ok(())
                } else {
                    Err("paste view is no longer focused".to_string())
                };
                let _ = reply.send(result);
            })
            .map_err(|error| error.to_string())?;
        receiver.await.map_err(|_| "paste view closed".to_string())?
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        let _ = (app, window);
        Err("system paste is unavailable on this platform".to_string())
    }
}

#[cfg(all(test, target_os = "linux"))]
mod tests_linux;
