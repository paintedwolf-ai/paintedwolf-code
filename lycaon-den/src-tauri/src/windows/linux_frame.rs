//! The frame around a Linux window that draws its own title bar.
//!
//! GTK draws the client-side frame: the shadow, outline, rounded corners, and
//! the resize border outside the content. The web view stays transparent so
//! the page can round its own corners to match, and the frame's fill shows
//! wherever the page has not painted yet.

use serde::Serialize;
#[cfg(any(target_os = "linux", test))]
use tauri::window::Color;
use tauri::WebviewWindow;

/// Event sent to a window when it starts or stops floating.
pub const FRAME_CHANGED_EVENT: &str = "linux-window-frame-changed";

/// Corner radius the frame and the page share; `--den-window-radius` mirrors it.
pub const CORNER_RADIUS_PX: u32 = 12;

/// How the window sits on the desktop.
#[derive(Debug, Clone, Copy, PartialEq, Serialize)]
pub struct WindowFrame {
    /// Not maximized, fullscreen, or tiled, so the frame shows corners and shadow.
    pub floating: bool,
    /// Content size in logical pixels, without the frame's shadow.
    pub content_width: f64,
    pub content_height: f64,
}

/// The window's frame state and content size.
#[tauri::command]
pub async fn linux_window_frame(window: WebviewWindow) -> Result<WindowFrame, String> {
    current_frame(window).await
}

#[cfg(not(target_os = "linux"))]
async fn current_frame(window: WebviewWindow) -> Result<WindowFrame, String> {
    let scale = window.scale_factor().map_err(|err| err.to_string())?;
    let size = window
        .inner_size()
        .map_err(|err| err.to_string())?
        .to_logical::<f64>(scale);
    Ok(WindowFrame {
        floating: !window.is_maximized().unwrap_or(false) && !window.is_fullscreen().unwrap_or(false),
        content_width: size.width,
        content_height: size.height,
    })
}

#[cfg(target_os = "linux")]
async fn current_frame(window: WebviewWindow) -> Result<WindowFrame, String> {
    let (sender, receiver) = std::sync::mpsc::channel();
    let target = window.clone();
    window
        .run_on_main_thread(move || {
            let _ = sender.send(target.gtk_window().map(|gtk| gtk_frame::frame_of(&gtk)));
        })
        .map_err(|err| err.to_string())?;
    tauri::async_runtime::spawn_blocking(move || receiver.recv())
        .await
        .map_err(|err| err.to_string())?
        .map_err(|err| err.to_string())?
        .map_err(|err| err.to_string())
}

/// Gives a window the client-side frame and reports its state changes.
#[cfg(target_os = "linux")]
pub fn install(window: &WebviewWindow) {
    let target = window.clone();
    let scheduled = window.run_on_main_thread(move || match target.gtk_window() {
        Ok(gtk) => gtk_frame::install(&target, &gtk),
        Err(err) => eprintln!("window frame: {err}"),
    });
    if let Err(err) = scheduled {
        eprintln!("window frame: {err}");
    }
}

/// Fills the frame behind the page with `color` and keeps the web view clear.
#[cfg(target_os = "linux")]
pub fn paint_backdrop(window: &WebviewWindow, color: Color) -> Result<(), String> {
    let target = window.clone();
    window
        .run_on_main_thread(move || match target.gtk_window() {
            Ok(gtk) => gtk_frame::paint(target.label(), &gtk, color),
            Err(err) => eprintln!("window frame: {err}"),
        })
        .map_err(|err| format!("window frame: {err}"))?;
    window
        .as_ref()
        .set_background_color(Some(Color(0, 0, 0, 0)))
        .map_err(|err| format!("webview backdrop: {err}"))
}

/// GTK CSS for the frame of a window filled with `color`.
#[cfg(any(target_os = "linux", test))]
fn frame_css(color: Color) -> String {
    let Color(red, green, blue, alpha) = color;
    let fill = format!("rgba({red},{green},{blue},{:.3})", f64::from(alpha) / 255.0);
    let radius = CORNER_RADIUS_PX;
    // A window without its shadow (maximized, fullscreen, tiled) has square corners.
    let fitted = ["maximized", "fullscreen", "tiled", "tiled-top", "tiled-right", "tiled-bottom", "tiled-left"]
        .map(|state| format!("window.{FRAME_CLASS}.{state}"));
    format!(
        "window.{FRAME_CLASS} {{ background-color: transparent; }}\n\
         window.{FRAME_CLASS} > decoration {{ border-radius: {radius}px; background-color: {fill}; }}\n\
         {} {{ background-color: {fill}; }}\n\
         {} {{ border-radius: 0; }}\n",
        fitted.join(", "),
        fitted.map(|selector| format!("{selector} > decoration")).join(", "),
    )
}

/// Style class that scopes the frame CSS to shell windows, not GTK dialogs.
#[cfg(any(target_os = "linux", test))]
const FRAME_CLASS: &str = "den-shell-window";

#[cfg(target_os = "linux")]
mod gtk_frame {
    use std::cell::RefCell;
    use std::collections::HashMap;

    use gtk::gdk::WindowState;
    use gtk::prelude::*;
    use tauri::window::Color;
    use tauri::{Emitter, WebviewWindow};

    use super::{frame_css, WindowFrame, FRAME_CHANGED_EVENT, FRAME_CLASS};

    thread_local! {
        /// One provider per window label, so a repaint replaces its own fill.
        static PROVIDERS: RefCell<HashMap<String, gtk::CssProvider>> = RefCell::new(HashMap::new());
    }

    const FITTED: WindowState = WindowState::MAXIMIZED
        .union(WindowState::FULLSCREEN)
        .union(WindowState::TILED)
        .union(WindowState::TOP_TILED)
        .union(WindowState::RIGHT_TILED)
        .union(WindowState::BOTTOM_TILED)
        .union(WindowState::LEFT_TILED);

    fn floating(state: WindowState) -> bool {
        !state.intersects(FITTED)
    }

    pub(super) fn frame_of(gtk: &gtk::ApplicationWindow) -> WindowFrame {
        let state = gtk.window().map(|window| window.state()).unwrap_or(WindowState::empty());
        // GTK's size excludes the shadow around client-side decorations.
        let (width, height) = gtk.size();
        WindowFrame {
            floating: floating(state),
            content_width: f64::from(width),
            content_height: f64::from(height),
        }
    }

    pub(super) fn install(window: &WebviewWindow, gtk: &gtk::ApplicationWindow) {
        gtk.style_context().add_class(FRAME_CLASS);
        // An empty title bar widget keeps GTK's frame without its header bar.
        if gtk.titlebar().is_none() {
            gtk.set_titlebar(Some(&gtk::EventBox::new()));
        }
        gtk.set_decorated(true);
        let window = window.clone();
        gtk.connect_window_state_event(move |gtk, event| {
            if event.changed_mask().intersects(FITTED) {
                let frame = WindowFrame {
                    floating: floating(event.new_window_state()),
                    ..frame_of(gtk)
                };
                if let Err(err) = window.emit_to(window.label(), FRAME_CHANGED_EVENT, frame) {
                    eprintln!("window frame: emit: {err}");
                }
            }
            gtk::glib::Propagation::Proceed
        });
    }

    pub(super) fn paint(label: &str, gtk: &gtk::ApplicationWindow, color: Color) {
        let provider = PROVIDERS.with(|providers| {
            providers
                .borrow_mut()
                .entry(label.to_string())
                .or_insert_with(|| {
                    let provider = gtk::CssProvider::new();
                    gtk.style_context()
                        .add_provider(&provider, gtk::STYLE_PROVIDER_PRIORITY_APPLICATION);
                    provider
                })
                .clone()
        });
        if let Err(err) = provider.load_from_data(frame_css(color).as_bytes()) {
            eprintln!("window frame: css: {err}");
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn only_a_free_window_floats() {
            assert!(floating(WindowState::empty()));
            assert!(floating(WindowState::FOCUSED));
            assert!(!floating(WindowState::MAXIMIZED | WindowState::FOCUSED));
            assert!(!floating(WindowState::FULLSCREEN));
            assert!(!floating(WindowState::LEFT_TILED | WindowState::TILED));
            assert!(!floating(WindowState::TOP_TILED));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_frame_fills_rounded_corners_and_squares_a_fitted_window() {
        let css = frame_css(Color(0x19, 0x18, 0x17, 0xff));
        assert!(css.contains("window.den-shell-window { background-color: transparent; }"));
        assert!(css.contains(
            "window.den-shell-window > decoration { border-radius: 12px; background-color: rgba(25,24,23,1.000); }"
        ));
        for state in ["maximized", "fullscreen", "tiled", "tiled-left"] {
            assert!(css.contains(&format!("window.den-shell-window.{state} > decoration")));
        }
        assert!(css.contains("window.den-shell-window.tiled-bottom, window.den-shell-window.tiled-left { background-color: rgba(25,24,23,1.000); }"));
    }

    /// The page rounds its corners with the same radius the frame uses.
    #[test]
    fn the_stylesheet_radius_matches_the_frame() {
        let manifest = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        let css = std::fs::read_to_string(manifest.join("../src/styling/shell/window-frame.css"))
            .expect("window frame stylesheet");
        assert!(
            css.contains(&format!("--den-window-radius: {CORNER_RADIUS_PX}px;")),
            "window-frame.css must declare --den-window-radius: {CORNER_RADIUS_PX}px"
        );
    }

    #[test]
    fn the_wire_shape_uses_snake_case_fields() {
        let json = serde_json::to_string(&WindowFrame {
            floating: true,
            content_width: 900.0,
            content_height: 600.0,
        })
        .unwrap();
        assert_eq!(json, r#"{"floating":true,"content_width":900.0,"content_height":600.0}"#);
    }
}
