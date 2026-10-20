//! Real WebKit paste events on the existing private X11/Wayland display fixtures.

use super::linux::{paste_into_view, GlibMainThread};
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
    images: Array.from(data.files).map(file => file.type)});
  event.preventDefault();
});
window.addEventListener('load', () => {
  document.querySelector('textarea').focus(); document.title = 'ready';
});
</script>"#;

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

pub(super) fn exercise() {
    tauri::async_runtime::block_on(async {
        for contents in [Contents::Plain, Contents::Rich, Contents::Image] {
            let (reply, receiver) = tokio::sync::oneshot::channel();
            GlibMainThread.dispatch(Box::new(move || {
                let window = gtk::Window::new(gtk::WindowType::Toplevel);
                window.set_default_size(320, 200);
                let view = webkit2gtk::WebView::new();
                window.add(&view);
                let reply = Rc::new(RefCell::new(Some(reply)));
                let answer = reply.clone();
                let owned_window = window.clone();
                view.connect_title_notify(move |view| {
                    let title = view.title().unwrap_or_default();
                    if title == "ready" {
                        view.grab_focus();
                        publish(contents);
                        if let Err(error) = paste_into_view(view) {
                            if let Some(reply) = answer.borrow_mut().take() {
                                let _ = reply.send(Err(error));
                            }
                            owned_window.close();
                        }
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
                        let _ = reply.send(Err("native paste event deadline".to_string()));
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
                    assert_eq!(observed["html"], "<b>fixture rich</b>");
                }
                Contents::Image => assert_eq!(observed["images"], serde_json::json!(["image/png"])),
            }
        }
    });
}
