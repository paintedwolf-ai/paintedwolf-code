//! GTK clipboard adapter over the application's own display connection.
//!
//! Works on Wayland and X11 alike without XWayland or compositor extensions:
//! the clipboard belongs to the display GTK already holds, so ownership lasts
//! as long as the process. Every toolkit call runs on the GTK main thread.

use gtk::gdk;
use gtk::prelude::*;
use tokio::sync::oneshot;

use super::service::{
    ClipboardAdapter, ClipboardError, ClipboardFacts, DisplayProtocol, MainThread, Reply,
};

#[derive(Default)]
pub(crate) struct NativeClipboard;

fn display() -> Result<gdk::Display, ClipboardError> {
    if !gtk::is_initialized_main_thread() {
        return Err(ClipboardError::DisplayUnavailable);
    }
    gdk::Display::default().ok_or(ClipboardError::DisplayUnavailable)
}

fn clipboard() -> Result<gtk::Clipboard, ClipboardError> {
    gtk::Clipboard::default(&display()?).ok_or(ClipboardError::DisplayUnavailable)
}

// GDK identifies its backend through the display's GObject type.
fn protocol(display: &gdk::Display) -> DisplayProtocol {
    match display.type_().name() {
        "GdkWaylandDisplay" => DisplayProtocol::Wayland,
        "GdkX11Display" => DisplayProtocol::X11,
        _ => DisplayProtocol::Gdk,
    }
}

/// Facts about the display connection; call on the GTK main thread.
pub(crate) fn startup_facts() -> Result<ClipboardFacts, ClipboardError> {
    let display = display()?;
    Ok(ClipboardFacts {
        protocol: protocol(&display),
        persistence: display.supports_clipboard_persistence(),
    })
}

fn on_main_thread<T: Send + 'static>(
    main: &dyn MainThread,
    task: impl FnOnce(oneshot::Sender<Result<T, ClipboardError>>) + Send + 'static,
) -> Reply<T> {
    let (reply, receiver) = oneshot::channel();
    // A refused dispatch drops the sender, which the service reports as stopped.
    let _ = main.dispatch(Box::new(move || task(reply)));
    receiver
}

impl ClipboardAdapter for NativeClipboard {
    fn write_text(&mut self, main: &dyn MainThread, text: String) -> Reply<()> {
        on_main_thread(main, move |reply| {
            let _ = reply.send(clipboard().map(|clipboard| clipboard.set_text(&text)));
        })
    }

    fn read_text(&mut self, main: &dyn MainThread) -> Reply<String> {
        on_main_thread(main, |reply| match clipboard() {
            Err(error) => {
                let _ = reply.send(Err(error));
            }
            Ok(clipboard) => clipboard.request_text(move |_, text| {
                let _ = reply.send(Ok(text.unwrap_or_default().to_owned()));
            }),
        })
    }

    fn release(&mut self, main: &dyn MainThread) {
        // Hands the text to a clipboard manager when the display has one;
        // otherwise ownership ends with the process, as for any GTK window.
        let _ = main.dispatch(Box::new(|| {
            if let Ok(clipboard) = clipboard() {
                clipboard.store();
            }
        }));
    }
}

/// Dispatches onto the GLib default main context, which `gtk::main` serves.
#[cfg(test)]
pub(crate) struct GlibMainThread;

#[cfg(test)]
impl MainThread for GlibMainThread {
    fn dispatch(&self, task: Box<dyn FnOnce() + Send + 'static>) -> Result<(), ClipboardError> {
        gtk::glib::MainContext::default().invoke(task);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn toolkit_work_off_the_main_thread_reports_the_display_unavailable() {
        assert_eq!(display().unwrap_err(), ClipboardError::DisplayUnavailable);
        assert_eq!(startup_facts().unwrap_err(), ClipboardError::DisplayUnavailable);
        let mut adapter = NativeClipboard;
        let reply = adapter.write_text(&Inline, "text".into());
        assert_eq!(
            reply.blocking_recv().unwrap(),
            Err(ClipboardError::DisplayUnavailable)
        );
        let reply = adapter.read_text(&Inline);
        assert_eq!(
            reply.blocking_recv().unwrap(),
            Err(ClipboardError::DisplayUnavailable)
        );
    }

    #[test]
    fn refused_dispatch_drops_the_reply() {
        let mut adapter = NativeClipboard;
        assert!(adapter.read_text(&Refusing).blocking_recv().is_err());
    }

    struct Inline;

    impl MainThread for Inline {
        fn dispatch(
            &self,
            task: Box<dyn FnOnce() + Send + 'static>,
        ) -> Result<(), ClipboardError> {
            task();
            Ok(())
        }
    }

    struct Refusing;

    impl MainThread for Refusing {
        fn dispatch(
            &self,
            _task: Box<dyn FnOnce() + Send + 'static>,
        ) -> Result<(), ClipboardError> {
            Err(ClipboardError::Stopped)
        }
    }
}
