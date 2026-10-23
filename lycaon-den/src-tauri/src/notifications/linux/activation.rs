use gtk::{glib::prelude::*, prelude::*};
use tauri::{AppHandle, Emitter, Manager};
use super::super::{ActivatedPayload, PresentationOwner, ACTIVATED_EVENT};

const WAYLAND_DISPLAY_TYPE: &str = "GdkWaylandDisplay";

/// How the main window is raised for an activation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Raise {
    /// A Wayland token activates the surface by itself; presenting as well
    /// would request a second token without an input serial.
    Token,
    /// X11 reads the token's timestamp and still needs the present call.
    TokenThenPresent,
    Present,
}

fn raise(wayland: bool, token: bool) -> Raise {
    match (wayland, token) {
        (true, true) => Raise::Token,
        (false, true) => Raise::TokenThenPresent,
        (_, false) => Raise::Present,
    }
}

pub(super) fn activate(app: &AppHandle, session_id: Option<String>, token: Option<String>) {
    let target = app.clone();
    let _ = app.run_on_main_thread(move || {
        if target
            .try_state::<super::NativeNotifications>()
            .is_none_or(|state| state.stopped.load(std::sync::atomic::Ordering::Acquire))
        {
            return;
        }
        if let Some(window) = target.get_webview_window("main") {
            let presentation = if present_native(&window, token.as_deref()).is_ok() {
                PresentationOwner::Native
            } else {
                PresentationOwner::Renderer
            };
            let _ = target.emit_to(
                "main",
                ACTIVATED_EVENT,
                ActivatedPayload {
                    session_id,
                    presentation,
                },
            );
        }
    });
}

fn present_native(window: &tauri::WebviewWindow, token: Option<&str>) -> Result<(), String> {
    let native = window.gtk_window().map_err(|error| error.to_string())?;
    let wayland = native.display().type_().name() == WAYLAND_DISPLAY_TYPE;
    // A closed workspace is hidden, not destroyed; set the token before mapping.
    match raise(wayland, token.is_some()) {
        Raise::Token => {
            native.set_startup_id(token.unwrap_or_default());
            if !native.is_visible() {
                window.show().map_err(|error| error.to_string())?;
            }
        }
        Raise::TokenThenPresent => {
            native.set_startup_id(token.unwrap_or_default());
            native.present();
        }
        Raise::Present => native.present(),
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wayland_tokens_activate_without_a_second_present() {
        assert_eq!(raise(true, true), Raise::Token);
        assert_eq!(raise(false, true), Raise::TokenThenPresent);
        assert_eq!(raise(true, false), Raise::Present);
        assert_eq!(raise(false, false), Raise::Present);
    }
}
