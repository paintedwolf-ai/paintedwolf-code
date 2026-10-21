//! Real WebKit paste events on the existing private X11/Wayland display fixtures.

use super::linux::{paste_into_view, request_image, GlibMainThread};
use super::service::MainThread;
use gtk::prelude::*;
use std::cell::RefCell;
use std::rc::Rc;
use std::time::Duration;
use webkit2gtk::WebViewExt;

const PAGE: &str = r#"<!doctype html><textarea autofocus></textarea><script>
document.addEventListener('paste', event => {
  const data = event.clipboardData;
  document.title = JSON.stringify({trusted: event.isTrusted,
    text: data.getData('text/plain'), html: data.getData('text/html'),
    images: Array.from(data.files).map(file => file.type),
    types: Array.from(data.types),
    items: Array.from(data.items).map(item => item.kind + ':' + item.type)});
  event.preventDefault();
});
window.addEventListener('load', () => {
  document.querySelector('textarea').focus(); document.title = 'ready';
});
</script>"#;

const WINDOW_TITLE: &str = "Painted Wolf Code native paste fixture";

#[derive(Clone, Copy)]
enum Contents {
    Plain,
    Rich,
    Image,
}

fn publish(contents: Contents) {
    let display = gtk::gdk::Display::default().expect("private display");
    let clipboard = gtk::Clipboard::default(&display).expect("private clipboard");
    match contents {
        Contents::Plain => clipboard.set_text("fixture plain"),
        Contents::Rich => {
            let targets = [
                gtk::TargetEntry::new("text/html", gtk::TargetFlags::empty(), 1),
                gtk::TargetEntry::new("UTF8_STRING", gtk::TargetFlags::empty(), 2),
            ];
            assert!(clipboard.set_with_data(&targets, |_, selection, info| {
                let bytes: &[u8] = if info == 1 {
                    b"<b>fixture rich</b>"
                } else {
                    b"fixture rich"
                };
                selection.set(&selection.target(), 8, bytes);
            }));
        }
        Contents::Image => {
            let image = gtk::gdk_pixbuf::Pixbuf::new(
                gtk::gdk_pixbuf::Colorspace::Rgb, false, 8, 2, 2,
            ).expect("fixture image");
            image.fill(0x2364aaff);
            clipboard.set_image(&image);
        }
    }
}

/// `focus_input` asks the compositor to focus the titled fixture window and
/// gives it a key event, so the view receives keyboard focus as a person's
/// keyboard would provide.
pub(super) fn exercise(focus_input: impl Fn(&str) + Clone + Send + 'static) {
    tauri::async_runtime::block_on(async {
        for contents in [Contents::Plain, Contents::Rich, Contents::Image] {
            let (reply, receiver) = tokio::sync::oneshot::channel();
            let focus_input = focus_input.clone();
            GlibMainThread.dispatch(Box::new(move || {
                let window = gtk::Window::new(gtk::WindowType::Toplevel);
                window.set_title(WINDOW_TITLE);
                window.set_default_size(320, 200);
                let view = webkit2gtk::WebView::new();
                window.add(&view);
                let reply = Rc::new(RefCell::new(Some(reply)));
                let progress = Rc::new(RefCell::new("loading"));
                let answer = reply.clone();
                let owned_window = window.clone();
                let stage = progress.clone();
                view.connect_title_notify(move |view| {
                    let title = view.title().unwrap_or_default();
                    if title == "ready" {
                        // The compositor activates the window asynchronously; paste
                        // once the view holds keyboard focus, as a menu action would.
                        let (view, window, answer, stage) = (view.clone(), owned_window.clone(), answer.clone(), stage.clone());
                        let focus_input = focus_input.clone();
                        gtk::glib::timeout_add_local(Duration::from_millis(50), move || {
                            if !view.has_focus() {
                                *stage.borrow_mut() = match (window.is_active(), window.has_toplevel_focus()) {
                                    (false, _) => "awaiting focus: window inactive",
                                    (true, false) => "awaiting focus: no toplevel focus",
                                    (true, true) => "awaiting focus: view unfocused",
                                };
                                window.present();
                                view.grab_focus();
                                focus_input(WINDOW_TITLE);
                                return gtk::glib::ControlFlow::Continue;
                            }
                            publish(contents);
                            *stage.borrow_mut() = "pasted";
                            if let Err(error) = paste_into_view(&view) {
                                if let Some(reply) = answer.borrow_mut().take() {
                                    let _ = reply.send(Err(error));
                                }
                                window.close();
                            }
                            gtk::glib::ControlFlow::Break
                        });
                    } else if title.starts_with('{') {
                        if let Some(reply) = answer.borrow_mut().take() {
                            let _ = reply.send(Ok(title.to_string()));
                        }
                        owned_window.close();
                    }
                });
                window.show_all();
                window.present();
                view.load_html(PAGE, Some("about:blank"));
                // The existing owner child deadline also kills a wedged web process.
                gtk::glib::timeout_add_local_once(Duration::from_secs(10), move || {
                    if let Some(reply) = reply.borrow_mut().take() {
                        let _ = reply.send(Err(format!("native paste event deadline while {}", progress.borrow())));
                    }
                    window.close();
                });
            })).expect("dispatch private native paste");
            let observed = tokio::time::timeout(Duration::from_secs(12), receiver)
                .await.expect("bounded native paste receiver")
                .expect("native paste sender").expect("native paste action");
            let observed: serde_json::Value = serde_json::from_str(&observed)
                .expect("typed native clipboard event");
            assert_eq!(observed["trusted"], true);
            match contents {
                Contents::Plain => assert_eq!(observed["text"], "fixture plain"),
                Contents::Rich => {
                    assert_eq!(observed["text"], "fixture rich");
                    // WebKit sanitizes pasted markup, adding computed inline style.
                    let html = observed["html"].as_str().expect("pasted markup");
                    assert!(html.starts_with("<b") && html.ends_with(">fixture rich</b>"), "pasted markup {html}");
                }
                Contents::Image => assert_clipboard_image().await,
            }
        }
    });
}

/// WebKitGTK hands the page no clipboard image, so the shell's read must
/// return the published pixels as PNG.
async fn assert_clipboard_image() {
    let (reply, receiver) = tokio::sync::oneshot::channel();
    GlibMainThread.dispatch(Box::new(move || request_image(reply))).expect("dispatch clipboard image read");
    let png = tokio::time::timeout(Duration::from_secs(5), receiver)
        .await.expect("bounded clipboard image read").expect("clipboard image sender").expect("clipboard image");
    assert!(png.starts_with(b"\x89PNG\r\n\x1a\n"), "clipboard image is PNG");
    let (reply, receiver) = tokio::sync::oneshot::channel();
    GlibMainThread.dispatch(Box::new(move || {
        let loader = gtk::gdk_pixbuf::PixbufLoader::new();
        let decoded = loader.write(&png).and_then(|_| loader.close()).ok().and_then(|_| loader.pixbuf())
            .map(|image| (image.width(), image.height(), image.read_pixel_bytes().to_vec()[..3].to_vec()));
        let _ = reply.send(decoded);
    })).expect("dispatch decode");
    let decoded = receiver.await.expect("decode reply").expect("decodable clipboard PNG");
    assert_eq!(decoded, (2, 2, vec![0x23, 0x64, 0xaa]));
}
