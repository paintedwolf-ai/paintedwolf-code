//! Wheel glides for every window of the app, reported to Den for scroll diagnostics.

use tauri::AppHandle;

/// Host event carrying one finished glide, for scroll diagnostics.
pub const GLIDE_EVENT: &str = "wheel://glide";

/// Installs wheel smoothing for every window of the app.
pub fn setup(app: AppHandle) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        use tauri::Emitter;
        wheel_glide::install(std::rc::Rc::new(move |report| {
            let _ = app.emit(GLIDE_EVENT, &report);
        }))
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        Ok(())
    }
}
