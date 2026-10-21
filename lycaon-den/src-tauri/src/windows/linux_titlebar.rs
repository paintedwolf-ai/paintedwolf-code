//! Title bar click actions the shell carries out natively on Linux.
//!
//! The page decides that a click hit the drawn title bar and which action the
//! desktop configures (see `linux_decorations::TitlebarActions`); lowering the
//! window and its window menu need the toolkit. The compositor shows a window
//! menu only for a real pointer press, so each web view records its latest
//! button press and the menu is anchored to it.

use tauri::WebviewWindow;

use super::linux_decorations::TitlebarAction;

#[cfg(target_os = "linux")]
mod native {
    use std::cell::RefCell;
    use std::collections::HashMap;
    use std::time::{Duration, Instant};

    use gtk::gdk;
    use gtk::prelude::*;
    use tauri::WebviewWindow;

    /// A press older than this no longer belongs to the click that asked for the menu.
    const PRESS_FRESHNESS: Duration = Duration::from_secs(2);

    thread_local! {
        static PRESSES: RefCell<HashMap<String, (gdk::Event, Instant)>> = RefCell::new(HashMap::new());
    }

    pub(super) fn install(window: &WebviewWindow) {
        let label = window.label().to_string();
        let result = window.with_webview(move |webview| {
            let widget: gtk::Widget = webview.inner().upcast();
            let label = label.clone();
            widget.connect_button_press_event(move |_, event| {
                PRESSES.with(|presses| {
                    presses.borrow_mut().insert(label.clone(), (event.clone().into(), Instant::now()));
                });
                gtk::glib::Propagation::Proceed
            });
        });
        if let Err(err) = result {
            eprintln!("title bar actions: {err}");
        }
        let label = window.label().to_string();
        window.on_window_event(move |event| {
            if matches!(event, tauri::WindowEvent::Destroyed) {
                forget(&label);
            }
        });
    }

    fn forget(label: &str) {
        PRESSES.with(|presses| presses.borrow_mut().remove(label));
    }

    pub(super) fn lower(window: &WebviewWindow) -> Result<(), String> {
        let gtk = window.gtk_window().map_err(|err| err.to_string())?;
        gtk.window().ok_or("the window is not shown")?.lower();
        Ok(())
    }

    pub(super) fn menu(window: &WebviewWindow) -> Result<(), String> {
        let gtk = window.gtk_window().map_err(|err| err.to_string())?;
        let surface = gtk.window().ok_or("the window is not shown")?;
        let press = PRESSES.with(|presses| {
            presses.borrow().get(window.label()).and_then(|(event, at)| {
                (at.elapsed() <= PRESS_FRESHNESS && event.button() == Some(3)).then(|| event.clone())
            })
        });
        let press = press.ok_or("no right-button press to anchor the window menu")?;
        if surface.show_window_menu(&press) {
            Ok(())
        } else {
            Err("the desktop offers no window menu".into())
        }
    }
}

/// Lowers the window or opens the desktop's window menu for a title bar click.
#[tauri::command]
pub fn linux_titlebar_action(window: WebviewWindow, action: TitlebarAction) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        match action {
            TitlebarAction::Lower => native::lower(&window),
            TitlebarAction::Menu => native::menu(&window),
            _ => Err("the page carries out this title bar action itself".into()),
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (window, action);
        Err("title bar actions are drawn by the system on this platform".into())
    }
}

/// Records presses in `window`'s web view for the window menu.
pub fn install(window: &WebviewWindow) {
    #[cfg(target_os = "linux")]
    native::install(window);
    #[cfg(not(target_os = "linux"))]
    let _ = window;
}
