//! Whether a screen reader is running, from AT-SPI's `org.a11y.Status` (Linux).
//!
//! WebKitGTK aborts its web process answering a screen reader's text-attribute
//! queries over text that cannot be selected, so Den makes text selectable
//! while one runs. The fact follows `ScreenReaderEnabled` on the session's
//! accessibility bus service, which the desktop sets while a screen reader runs.

use serde::Serialize;
use std::sync::Mutex;
use tauri::AppHandle;

pub const SCREEN_READER_CHANGED_EVENT: &str = "accessibility://screen-reader-changed";

/// Screen reader fact; `revision` orders the boot read against later changes.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScreenReaderPayload {
    pub active: bool,
    pub revision: u64,
}

static CURRENT: Mutex<ScreenReaderPayload> = Mutex::new(ScreenReaderPayload {
    active: false,
    revision: 0,
});

/// Records a reading; returns the payload to emit when the fact changed.
fn record(state: &Mutex<ScreenReaderPayload>, active: bool) -> Option<ScreenReaderPayload> {
    let mut current = state.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    if current.active == active && current.revision > 0 {
        return None;
    }
    *current = ScreenReaderPayload {
        active,
        revision: current.revision + 1,
    };
    Some(*current)
}

#[tauri::command]
pub fn accessibility_screen_reader() -> ScreenReaderPayload {
    *CURRENT.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

/// Follows the screen reader fact where the platform publishes one.
pub fn setup(app: AppHandle) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        linux::watch(app)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = app;
        Ok(())
    }
}

#[cfg(target_os = "linux")]
mod linux {
    use gtk::gio;
    use gtk::gio::prelude::*;
    use gtk::glib;
    use tauri::{AppHandle, Emitter};

    use super::{record, CURRENT, SCREEN_READER_CHANGED_EVENT};

    const PROPERTY: &str = "ScreenReaderEnabled";

    /// A missing or mistyped property means no screen reader is running.
    pub(super) fn active(value: Option<glib::Variant>) -> bool {
        value.and_then(|value| value.get::<bool>()).unwrap_or(false)
    }

    fn publish(app: &AppHandle, proxy: &gio::DBusProxy) {
        if let Some(payload) = record(&CURRENT, active(proxy.cached_property(PROPERTY))) {
            let _ = app.emit(SCREEN_READER_CHANGED_EVENT, payload);
        }
    }

    pub(super) fn watch(app: AppHandle) -> Result<(), String> {
        std::thread::Builder::new()
            .name("den-screen-reader".into())
            .spawn(move || {
                let context = glib::MainContext::new();
                let main_loop = glib::MainLoop::new(Some(&context), false);
                let started = context.with_thread_default(|| {
                    let connection = crate::linux_desktop::session_bus::connect(&context)?;
                    // The proxy follows the bus service as it starts and stops, refreshing its cache.
                    let proxy = gio::DBusProxy::new_sync(
                        &connection,
                        gio::DBusProxyFlags::DO_NOT_AUTO_START,
                        None,
                        Some("org.a11y.Bus"),
                        "/org/a11y/bus",
                        "org.a11y.Status",
                        gio::Cancellable::NONE,
                    )
                    .map_err(|error| error.to_string())?;
                    publish(&app, &proxy);
                    let changes = app.clone();
                    // gio's bindings leave this signal unwrapped; the cache is updated before it fires.
                    proxy.connect_local("g-properties-changed", false, move |args| {
                        if let Some(proxy) = args.first().and_then(|value| value.get::<gio::DBusProxy>().ok()) {
                            publish(&changes, &proxy);
                        }
                        None
                    });
                    Ok::<_, String>(proxy)
                });
                match started {
                    // The proxy lives as long as the loop that delivers its signals.
                    Ok(Ok(_proxy)) => main_loop.run(),
                    Ok(Err(error)) => eprintln!("screen reader fact unavailable: {error}"),
                    Err(error) => eprintln!("screen reader fact unavailable: {error}"),
                }
            })
            .map(|_| ())
            .map_err(|error| error.to_string())
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn reads_a_running_screen_reader_from_the_property() {
            assert!(active(Some(true.to_variant())));
            assert!(!active(Some(false.to_variant())));
        }

        #[test]
        fn treats_a_missing_or_mistyped_property_as_none_running() {
            assert!(!active(None));
            assert!(!active(Some("yes".to_variant())));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_first_reading_publishes_even_when_inactive() {
        let state = Mutex::new(ScreenReaderPayload::default());
        assert_eq!(
            record(&state, false),
            Some(ScreenReaderPayload { active: false, revision: 1 })
        );
    }

    #[test]
    fn only_changes_publish_and_each_advances_the_revision() {
        let state = Mutex::new(ScreenReaderPayload::default());
        record(&state, false);
        assert_eq!(record(&state, false), None);
        assert_eq!(
            record(&state, true),
            Some(ScreenReaderPayload { active: true, revision: 2 })
        );
        assert_eq!(record(&state, true), None);
        assert_eq!(
            record(&state, false),
            Some(ScreenReaderPayload { active: false, revision: 3 })
        );
    }

    #[test]
    fn the_payload_serializes_in_camel_case() {
        let json = serde_json::to_string(&ScreenReaderPayload { active: true, revision: 4 }).unwrap();
        assert_eq!(json, r#"{"active":true,"revision":4}"#);
    }
}
