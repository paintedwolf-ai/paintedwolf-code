use gtk::prelude::*;
use tauri::{AppHandle, Emitter, Manager};

pub(super) fn activate(app: &AppHandle, session_id: Option<String>, token: Option<String>) {
    #[derive(Clone, serde::Serialize)]
    #[serde(rename_all = "camelCase")]
    struct Activated {
        session_id: Option<String>,
    }
    let target = app.clone();
    let _ = app.run_on_main_thread(move || {
        if let Some(window) = target.get_webview_window("main") {
            if let Ok(native) = window.gtk_window() {
                if let Some(token) = token {
                    native.set_startup_id(&token);
                }
                native.present();
            }
            let _ = target.emit_to(
                "main",
                "notifications://activated",
                Activated { session_id },
            );
        }
    });
}
