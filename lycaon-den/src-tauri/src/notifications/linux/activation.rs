use gtk::{glib::prelude::*, prelude::*};
use tauri::{AppHandle, Emitter, Manager};

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
    #[derive(Clone, serde::Serialize)]
    #[serde(rename_all = "camelCase")]
    struct Activated {
        session_id: Option<String>,
    }
    let target = app.clone();
    let _ = app.run_on_main_thread(move || {
        if target
            .try_state::<super::NativeNotifications>()
            .is_none_or(|state| state.stopped.load(std::sync::atomic::Ordering::Acquire))
        {
            return;
        }
        if let Some(window) = target.get_webview_window("main") {
            if let Ok(native) = window.gtk_window() {
                let wayland = native.display().type_().name() == WAYLAND_DISPLAY_TYPE;
                match raise(wayland, token.is_some()) {
                    Raise::Token => native.set_startup_id(token.as_deref().unwrap_or_default()),
                    Raise::TokenThenPresent => {
                        native.set_startup_id(token.as_deref().unwrap_or_default());
                        native.present();
                    }
                    Raise::Present => native.present(),
                }
            }
            let _ = target.emit_to(
                "main",
                "notifications://activated",
                Activated { session_id },
            );
        }
    });
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
