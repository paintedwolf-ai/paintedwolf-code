//! Initial lock facts, native signals and bounded bus reconnection.

use std::cell::RefCell;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

use gtk::glib::ToVariant;
use gtk::{gio, glib};
use tauri::AppHandle;

use super::{lock_in_background, LockReason};
use crate::linux_desktop::{bus, session};

mod state;
pub(crate) use state::LockSources;

static SOURCES: Mutex<LockSources> = Mutex::new(LockSources {
    session: None,
    session_error: None,
    screensaver_locked: None,
    system_connected: false,
    session_bus_connected: false,
    sleeping: false,
});
static REFRESH: AtomicBool = AtomicBool::new(false);
static REFRESHING: AtomicBool = AtomicBool::new(false);

#[derive(Default)]
struct Monitor {
    installed: bool,
    system: Option<gio::DBusConnection>,
    desktop: Option<gio::DBusConnection>,
    subscriptions: Vec<(gio::DBusConnection, gio::SignalSubscriptionId)>,
}

thread_local! { static MONITOR: RefCell<Monitor> = RefCell::new(Monitor::default()); }

pub(crate) fn status() -> LockSources {
    SOURCES.lock().unwrap().clone()
}
pub(crate) fn confirmation_allowed() -> bool {
    SOURCES.lock().unwrap().confirmation_allowed()
}

pub(super) fn setup_linux(app: AppHandle) -> Result<(), String> {
    let installed = MONITOR.with(|monitor| {
        let mut monitor = monitor.borrow_mut();
        std::mem::replace(&mut monitor.installed, true)
    });
    if installed {
        return Ok(());
    }
    refresh(&app);
    glib::timeout_add_seconds_local(5, move || {
        if REFRESHING.load(Ordering::Acquire) {
            return glib::ControlFlow::Continue;
        }
        let reconnect = MONITOR.with(|monitor| {
            let monitor = monitor.borrow();
            monitor.system.as_ref().is_none_or(|bus| bus.is_closed())
                || monitor.desktop.as_ref().is_none_or(|bus| bus.is_closed())
        });
        if reconnect || REFRESH.swap(false, Ordering::AcqRel) {
            refresh(&app);
        }
        glib::ControlFlow::Continue
    });
    Ok(())
}

fn refresh(app: &AppHandle) {
    if REFRESHING.swap(true, Ordering::AcqRel) {
        REFRESH.store(true, Ordering::Release);
        return;
    }
    struct RefreshGuard;
    impl Drop for RefreshGuard {
        fn drop(&mut self) { REFRESHING.store(false, Ordering::Release); }
    }
    let _refreshing = RefreshGuard;
    let context = glib::MainContext::default();
    let timeout = Duration::from_millis(1500);
    MONITOR.with(|monitor| {
        let mut monitor = monitor.borrow_mut();
        for (connection, id) in monitor.subscriptions.drain(..) {
            connection.signal_unsubscribe(id);
        }
        if monitor.system.as_ref().is_none_or(|bus| bus.is_closed()) {
            monitor.system = bus::connect(&context, bus::SYSTEM_BUS_ADDRESS, timeout).ok();
            if let Some(system) = &monitor.system {
                watch_connection(system, app, true);
            }
        }
        if monitor.desktop.as_ref().is_none_or(|bus| bus.is_closed()) {
            monitor.desktop = bus::connect_session(&context, timeout).ok();
            if let Some(desktop) = &monitor.desktop {
                watch_connection(desktop, app, false);
            }
        }
        let mut current = status();
        current.system_connected = monitor.system.is_some();
        current.session_bus_connected = monitor.desktop.is_some();
        current.session = None;
        current.session_error = None;
        current.screensaver_locked = None;
        if let Some(system) = monitor.system.clone() {
            if let Ok(reply) = bus::call(
                &context,
                &system,
                Some(session::NAME),
                "/org/freedesktop/login1",
                "org.freedesktop.DBus.Properties",
                "Get",
                Some(&("org.freedesktop.login1.Manager", "PreparingForSleep").to_variant()),
                Duration::from_secs(2),
            ) {
                if reply.is_type(glib::VariantTy::new("(v)").expect("property reply")) {
                    if let Some(sleeping) = reply
                        .child_value(0)
                        .as_variant()
                        .and_then(|value| value.get::<bool>())
                    {
                        current.sleeping = sleeping;
                    }
                }
            }
            match session::discover(&context, &system) {
                Ok(session) => current.session = Some(session),
                Err(error) => current.session_error = Some(error),
            }
            subscribe_system(
                &mut monitor,
                &system,
                app,
                current
                    .session
                    .as_ref()
                    .map(|session| session.path.as_str()),
            );
        }
        if let Some(desktop) = monitor.desktop.clone() {
            for (name, path) in [
                (
                    "org.freedesktop.ScreenSaver",
                    "/org/freedesktop/ScreenSaver",
                ),
                ("org.gnome.ScreenSaver", "/org/gnome/ScreenSaver"),
            ] {
                if let Ok(reply) = bus::call(
                    &context,
                    &desktop,
                    Some(name),
                    path,
                    name,
                    "GetActive",
                    None,
                    Duration::from_secs(2),
                ) {
                    if reply.is_type(glib::VariantTy::new("(b)").expect("bool reply")) {
                        current.screensaver_locked = reply.child_value(0).get::<bool>();
                        subscribe_screensaver(&mut monitor, &desktop, app, name, path);
                        break;
                    }
                }
            }
            watch_owner(&mut monitor, &desktop, "org.freedesktop.ScreenSaver", app);
            watch_owner(&mut monitor, &desktop, "org.gnome.ScreenSaver", app);
        }
        apply(app, current, LockReason::ScreenLocked);
    });
}

fn apply(app: &AppHandle, next: LockSources, reason: LockReason) {
    let mut sources = SOURCES.lock().unwrap();
    let was_allowed = sources.confirmation_allowed();
    let now_allowed = next.confirmation_allowed();
    let locked = next.sleeping
        || next
            .session
            .as_ref()
            .is_some_and(|session| !session.active || session.locked)
        || next.screensaver_locked == Some(true);
    *sources = next;
    eprintln!(
        "Step-away capability facts: {}",
        serde_json::to_string(&*sources).unwrap_or_default()
    );
    drop(sources);
    if !now_allowed && (was_allowed || locked) {
        lock_in_background(app, reason);
    }
}

fn watch_connection(connection: &gio::DBusConnection, app: &AppHandle, system: bool) {
    let app = app.clone();
    connection.connect_closed(move |_, _, _| {
        let mut next = status();
        if system {
            next.system_connected = false;
            next.session = None;
        } else {
            next.session_bus_connected = false;
            next.screensaver_locked = None;
        }
        apply(&app, next, LockReason::ScreenLocked);
        lock_in_background(&app, LockReason::ScreenLocked);
        REFRESH.store(true, Ordering::Release);
    });
}

fn watch_owner(
    monitor: &mut Monitor,
    connection: &gio::DBusConnection,
    name: &str,
    app: &AppHandle,
) {
    let app = app.clone();
    let system = name == session::NAME;
    let id = connection.signal_subscribe(
        Some("org.freedesktop.DBus"),
        Some("org.freedesktop.DBus"),
        Some("NameOwnerChanged"),
        Some("/org/freedesktop/DBus"),
        Some(name),
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, _| {
            let mut next = status();
            if system {
                next.session = None;
                next.session_error = Some(session::SessionUnavailable::Unreadable);
            } else {
                next.screensaver_locked = None;
            }
            apply(&app, next, LockReason::ScreenLocked);
            lock_in_background(&app, LockReason::ScreenLocked);
            REFRESH.store(true, Ordering::Release);
        },
    );
    monitor.subscriptions.push((connection.clone(), id));
}

fn subscribe_system(
    monitor: &mut Monitor,
    connection: &gio::DBusConnection,
    app: &AppHandle,
    path: Option<&str>,
) {
    watch_owner(monitor, connection, session::NAME, app);
    let owner = app.clone();
    let id = connection.signal_subscribe(
        Some(session::NAME),
        Some("org.freedesktop.login1.Manager"),
        Some("PrepareForSleep"),
        Some("/org/freedesktop/login1"),
        None,
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if !parameters.is_type(glib::VariantTy::new("(b)").expect("bool signal")) {
                return;
            }
            let mut next = status();
            next.sleeping = parameters.child_value(0).get::<bool>().unwrap_or(true);
            if !next.sleeping {
                next.session = None;
                next.screensaver_locked = None;
                REFRESH.store(true, Ordering::Release);
            }
            apply(&owner, next, LockReason::Sleep);
        },
    );
    monitor.subscriptions.push((connection.clone(), id));
    let Some(path) = path else {
        return;
    };
    for (member, locked) in [("Lock", true), ("Unlock", false)] {
        let owner = app.clone();
        let id = connection.signal_subscribe(
            Some(session::NAME),
            Some(session::SESSION_INTERFACE),
            Some(member),
            Some(path),
            None,
            gio::DBusSignalFlags::NONE,
            move |_, _, _, _, _, _| {
                let mut next = status();
                if let Some(session) = &mut next.session {
                    session.locked = locked;
                }
                apply(&owner, next, LockReason::ScreenLocked);
            },
        );
        monitor.subscriptions.push((connection.clone(), id));
    }
    let owner = app.clone();
    let id = connection.signal_subscribe(
        Some(session::NAME),
        Some("org.freedesktop.DBus.Properties"),
        Some("PropertiesChanged"),
        Some(path),
        Some(session::SESSION_INTERFACE),
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if !parameters.is_type(glib::VariantTy::new("(sa{sv}as)").expect("property signal")) {
                return;
            }
            let changed = glib::VariantDict::new(Some(&parameters.child_value(1)));
            let mut next = status();
            if let Some(session) = &mut next.session {
                if let Ok(Some(locked)) = changed.lookup::<bool>("LockedHint") {
                    session.locked = locked;
                }
                if let Ok(Some(active)) = changed.lookup::<bool>("Active") {
                    session.active = active;
                }
                if let Ok(Some(can_lock)) = changed.lookup::<bool>("CanLock") {
                    session.can_lock = can_lock;
                }
            }
            if parameters.child_value(2).n_children() > 0
                || ["Type", "Class", "User"]
                    .iter()
                    .any(|key| changed.lookup_value(key, None).is_some())
            {
                next.session = None;
                next.session_error = Some(session::SessionUnavailable::Unreadable);
                lock_in_background(&owner, LockReason::ScreenLocked);
                REFRESH.store(true, Ordering::Release);
            }
            apply(&owner, next, LockReason::ScreenLocked);
        },
    );
    monitor.subscriptions.push((connection.clone(), id));
}

fn subscribe_screensaver(
    monitor: &mut Monitor,
    connection: &gio::DBusConnection,
    app: &AppHandle,
    name: &str,
    path: &str,
) {
    let app = app.clone();
    let id = connection.signal_subscribe(
        Some(name),
        Some(name),
        Some("ActiveChanged"),
        Some(path),
        None,
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if !parameters.is_type(glib::VariantTy::new("(b)").expect("bool signal")) {
                return;
            }
            let mut next = status();
            next.screensaver_locked = parameters.child_value(0).get::<bool>();
            apply(&app, next, LockReason::ScreenLocked);
        },
    );
    monitor.subscriptions.push((connection.clone(), id));
}
