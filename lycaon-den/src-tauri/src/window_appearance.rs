//! Hidden-until-styled window creation and main-window close policy.

use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;
use std::time::Duration;

use tauri::window::Color;
use tauri::{AppHandle, LogicalPosition, LogicalSize, Manager, Theme, WebviewWindow, WindowEvent};

use crate::window_backdrop;

const MAIN_WINDOW_LABEL: &str = "main";

/// Quit must close the main window; a workspace close only hides it.
static QUIT_REQUESTED: AtomicBool = AtomicBool::new(false);

/// Reveals a window if its document never reports a paint.
const REVEAL_FALLBACK: Duration = Duration::from_millis(2500);

/// Backdrop for a window that does not exist yet.
pub fn background_for_new_window(app: &AppHandle) -> Color {
    let theme = app
        .get_webview_window(MAIN_WINDOW_LABEL)
        .and_then(|main| main.theme().ok())
        .unwrap_or(Theme::Light);
    window_backdrop::backdrop_for_os_theme(theme)
}

pub fn setup(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) else {
        return;
    };
    prepare(&window);
    keep_main_workspace_alive(&window);
}

/// Hide the main window on close; dock reopen restores it.
#[cfg(target_os = "macos")]
fn keep_main_workspace_alive(window: &WebviewWindow) {
    let main = window.clone();
    window.on_window_event(move |event| {
        if let WindowEvent::CloseRequested { api, .. } = event {
            if !QUIT_REQUESTED.load(Ordering::Relaxed) {
                api.prevent_close();
                let _ = main.hide();
            }
        }
    });
}

#[cfg(not(target_os = "macos"))]
fn keep_main_workspace_alive(_window: &WebviewWindow) {}

pub fn prepare_to_quit() {
    QUIT_REQUESTED.store(true, Ordering::Relaxed);
}

/// Restores the main window from the dock.
#[cfg(target_os = "macos")]
pub fn restore_main_workspace(app: &AppHandle) {
    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
        show_window(&window);
    }
}

/// Applies the backdrop and starts the reveal fallback.
pub fn prepare(window: &WebviewWindow) {
    apply_backdrop(
        window,
        window_backdrop::backdrop_for_os_theme(resolved_theme(window)),
    );

    let themed = window.clone();
    window.on_window_event(move |event| {
        if let WindowEvent::ThemeChanged(theme) = event {
            let memo = window_backdrop::current_memo();
            if window_backdrop::follows_os_appearance(&memo) {
                apply_backdrop(
                    &themed,
                    window_backdrop::color_from_memo(
                        &memo,
                        window_backdrop::Scheme::from_theme(*theme),
                    ),
                );
            }
        }
    });

    let fallback = window.clone();
    thread::spawn(move || {
        thread::sleep(REVEAL_FALLBACK);
        show_window(&fallback);
    });
}

fn apply_backdrop(window: &WebviewWindow, color: Color) {
    if let Err(err) = window_backdrop::apply_to_window(window, color) {
        eprintln!("{err}");
    }
}

/// This window's appearance, or main's if it cannot report one.
fn resolved_theme(window: &WebviewWindow) -> Theme {
    if let Ok(theme) = window.theme() {
        return theme;
    }
    window
        .app_handle()
        .get_webview_window(MAIN_WINDOW_LABEL)
        .and_then(|main| main.theme().ok())
        .unwrap_or(Theme::Light)
}

/// Marks the document painted and reveals its window.
#[tauri::command]
pub fn den_reveal_window(window: WebviewWindow) {
    if window_backdrop::mark_painted(window.label()) {
        apply_backdrop(
            &window,
            window_backdrop::backdrop_for_os_theme(resolved_theme(&window)),
        );
    }
    show_window(&window);
}

/// Moves and resizes a window in one native step.
#[tauri::command]
pub fn den_set_window_bounds(
    window: WebviewWindow,
    x: f64,
    y: f64,
    width: f64,
    height: f64,
) -> Result<(), String> {
    let finite = [x, y, width, height].iter().all(|value| value.is_finite());
    if !finite || width <= 0.0 || height <= 0.0 {
        return Err("invalid window bounds".into());
    }
    window
        .set_position(LogicalPosition::new(x, y))
        .map_err(|err| err.to_string())?;
    window
        .set_size(LogicalSize::new(width, height))
        .map_err(|err| err.to_string())
}

fn show_window(window: &WebviewWindow) {
    if window.is_visible().unwrap_or(false) {
        return;
    }
    let _ = window.show();
    let _ = window.set_focus();
}
