//! Main workspace close, reopen, and activation.
//!
//! The shell keeps the main workspace window, rather than destroying it, while
//! the application can bring it back: the dock on macOS, and on Linux the
//! launcher (a second launch activates the running GApplication) and
//! notifications while another Den window stays open. Closing the last Linux
//! window is a quit, routed through the exit transaction so work is preserved
//! and "Keep working" leaves the workspace usable.

use std::sync::atomic::{AtomicBool, Ordering};

use tauri::{AppHandle, Manager, WebviewWindow, WindowEvent};

const MAIN_WINDOW_LABEL: &str = "main";

/// Set once the exit transaction approves quitting; the main window may then close.
static QUIT_REQUESTED: AtomicBool = AtomicBool::new(false);

/// What a close request on the main workspace does.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum MainClose {
    /// Hide the window; activation restores it.
    Hide,
    /// Start the application exit transaction; the window stays until it commits.
    Quit,
    /// Let the window close.
    Close,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Platform {
    MacOs,
    Linux,
    Other,
}

const PLATFORM: Platform = if cfg!(target_os = "macos") {
    Platform::MacOs
} else if cfg!(target_os = "linux") {
    Platform::Linux
} else {
    Platform::Other
};

fn main_close(platform: Platform, quitting: bool, other_visible_windows: bool) -> MainClose {
    if quitting {
        return MainClose::Close;
    }
    match platform {
        Platform::MacOs => MainClose::Hide,
        Platform::Linux if other_visible_windows => MainClose::Hide,
        Platform::Linux => MainClose::Quit,
        Platform::Other => MainClose::Close,
    }
}

/// Whether the shell, not the document, ends this window's close request.
fn retains(platform: Platform, label: &str, quitting: bool) -> bool {
    label == MAIN_WINDOW_LABEL && main_close(platform, quitting, false) != MainClose::Close
}

pub fn setup(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) else {
        return;
    };
    let main = window.clone();
    window.on_window_event(move |event| {
        if let WindowEvent::CloseRequested { api, .. } = event {
            let app = main.app_handle();
            let others = app
                .webview_windows()
                .values()
                .any(|other| other.label() != MAIN_WINDOW_LABEL && other.is_visible().unwrap_or(false));
            match main_close(PLATFORM, QUIT_REQUESTED.load(Ordering::Acquire), others) {
                MainClose::Hide => {
                    api.prevent_close();
                    let _ = main.hide();
                }
                MainClose::Quit => {
                    api.prevent_close();
                    app.exit(0);
                }
                MainClose::Close => {}
            }
        }
    });
    #[cfg(target_os = "linux")]
    follow_application_activation(app, &window);
}

/// A second launch, from the launcher or a terminal, forwards activation to the
/// running GApplication and exits; the running instance restores its workspace.
#[cfg(target_os = "linux")]
fn follow_application_activation(app: &AppHandle, window: &WebviewWindow) {
    use gtk::prelude::*;
    let Some(application) = window.gtk_window().ok().and_then(|native| native.application()) else {
        eprintln!("The main window has no application; launcher activation cannot restore it.");
        return;
    };
    let target = app.clone();
    application.connect_activate(move |_| restore_main_workspace(&target));
}

pub fn prepare_to_quit() {
    QUIT_REQUESTED.store(true, Ordering::Release);
}

/// Answers a window's close request after the document preserved its state:
/// `retained` when the shell keeps the window, `destroy` when the document
/// releases its resources and destroys it.
#[tauri::command]
pub fn den_window_close_disposition(window: WebviewWindow) -> &'static str {
    if retains(PLATFORM, window.label(), QUIT_REQUESTED.load(Ordering::Acquire)) {
        "retained"
    } else {
        "destroy"
    }
}

/// Shows and raises the main workspace. On Linux, GTK presents with the
/// activation token the launcher forwarded with the activation, if any.
pub fn restore_main_workspace(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) else {
        return;
    };
    #[cfg(target_os = "linux")]
    {
        use gtk::prelude::*;
        let _ = window.show();
        if let Ok(native) = window.gtk_window() {
            native.present();
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        if !window.is_visible().unwrap_or(false) {
            let _ = window.show();
        }
        let _ = window.set_focus();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn macos_hides_the_workspace_until_quit() {
        assert_eq!(main_close(Platform::MacOs, false, false), MainClose::Hide);
        assert_eq!(main_close(Platform::MacOs, false, true), MainClose::Hide);
        assert_eq!(main_close(Platform::MacOs, true, false), MainClose::Close);
    }

    #[test]
    fn linux_hides_beside_another_window_and_quits_as_the_last() {
        assert_eq!(main_close(Platform::Linux, false, true), MainClose::Hide);
        assert_eq!(main_close(Platform::Linux, false, false), MainClose::Quit);
        assert_eq!(main_close(Platform::Linux, true, true), MainClose::Close);
        assert_eq!(main_close(Platform::Other, false, true), MainClose::Close);
    }

    #[test]
    fn only_a_retained_main_workspace_skips_document_destruction() {
        assert!(retains(Platform::Linux, MAIN_WINDOW_LABEL, false));
        assert!(retains(Platform::MacOs, MAIN_WINDOW_LABEL, false));
        assert!(!retains(Platform::Linux, MAIN_WINDOW_LABEL, true));
        assert!(!retains(Platform::Linux, "item-1", false));
        assert!(!retains(Platform::Other, MAIN_WINDOW_LABEL, false));
    }
}
