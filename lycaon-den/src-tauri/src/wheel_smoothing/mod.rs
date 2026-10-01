//! Smooth scrolling for notched mouse wheels.
//!
//! WebKit on macOS applies each notch at once and offers no embedder switch, so
//! the macOS host replays a notch over web content as a glide of pixel scroll
//! events that WebKit scrolls natively. Trackpad, Magic Mouse, and momentum
//! input pass through untouched.

pub mod glide;
#[cfg(target_os = "macos")]
mod macos;

#[cfg(target_os = "macos")]
pub use macos::install;

use tauri::AppHandle;

/// Host event carrying one finished glide, for scroll diagnostics.
pub const GLIDE_EVENT: &str = "wheel://glide";

/// Installs wheel smoothing for every window of the app.
pub fn setup(app: AppHandle) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        use tauri::Emitter;
        install(std::rc::Rc::new(move |report| {
            let _ = app.emit(GLIDE_EVENT, &report);
        }))
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        Ok(())
    }
}
