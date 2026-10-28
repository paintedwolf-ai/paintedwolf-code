//! Plain-text clipboard access for copy, cut, and paste actions.
//!
//! Every window shares one serialized service over the platform adapter:
//! GTK on Linux, the OS pasteboard elsewhere. Shutdown is terminal.

mod service;
mod secret;
#[cfg(target_os = "macos")]
mod secret_macos;
#[cfg(target_os = "windows")]
mod secret_windows;
#[cfg(target_os = "linux")]
mod secret_linux;

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

#[tauri::command]
pub async fn write_secret_text(
    text: String,
    app: tauri::AppHandle,
    state: tauri::State<'_, ClipboardState>,
) -> Result<u64, ClipboardError> {
    state.service.write_secret(&app, text).await
}

#[tauri::command]
pub async fn clear_secret_text(
    copy: u64,
    app: tauri::AppHandle,
    state: tauri::State<'_, ClipboardState>,
) -> Result<(), ClipboardError> {
    state.service.clear_secret(&app, copy).await
}

/// Empty when the board has no text.
#[tauri::command]
pub async fn read_clipboard_text(
    app: tauri::AppHandle,
    state: tauri::State<'_, ClipboardState>,
) -> Result<String, ClipboardError> {
    state.service.read(&app).await
}

/// The clipboard image as PNG bytes, empty when it holds none. Only Linux needs
/// it: WebKitGTK paste events carry no clipboard images, while WebKit on macOS
/// and WebView2 deliver them with the event.
#[tauri::command]
pub async fn read_clipboard_image(app: tauri::AppHandle) -> Result<tauri::ipc::Response, ClipboardError> {
    #[cfg(target_os = "linux")]
    {
        let (reply, receiver) = tokio::sync::oneshot::channel();
        app.run_on_main_thread(move || linux::request_image(reply))
            .map_err(|_| ClipboardError::Stopped)?;
        let png = tokio::time::timeout(service::OPERATION_TIMEOUT, receiver)
            .await
            .map_err(|_| ClipboardError::Timeout)?
            .map_err(|_| ClipboardError::Stopped)??;
        Ok(tauri::ipc::Response::new(png))
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = app;
        Err(ClipboardError::Backend("clipboard images arrive with the paste event on this platform".into()))
    }
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
        let _ = app;
        let (reply, receiver) = tokio::sync::oneshot::channel();
        window
            .with_webview(move |view| {
                let _ = reply.send(linux::paste_into_view(&view.inner()));
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

#[cfg(all(test, target_os = "linux"))]
mod tests_native_paste;
