//! Bus subscriptions and reads that feed the step-away monitor.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Weak;
use std::sync::atomic::{AtomicU64, Ordering};

use gtk::glib::ToVariant;
use gtk::{gio, glib};

use super::{Buses, Inner, CALL_TIMEOUT};
use crate::linux_desktop::{bus, session};
use crate::vault_lock::LockReason;

pub(super) fn preparing_for_sleep(
    context: &glib::MainContext,
    system: &gio::DBusConnection,
) -> Option<bool> {
    let reply = bus::call(
        context,
        system,
        Some(session::NAME),
        "/org/freedesktop/login1",
        "org.freedesktop.DBus.Properties",
        "Get",
        Some(&("org.freedesktop.login1.Manager", "PreparingForSleep").to_variant()),
        CALL_TIMEOUT,
    )
    .ok()?;
    if !reply.is_type(glib::VariantTy::new("(v)").ok()?) {
        return None;
    }
    reply.child_value(0).as_variant()?.get::<bool>()
}

pub(super) fn screensaver_active(
    context: &glib::MainContext,
    desktop: &gio::DBusConnection,
    name: &str,
    path: &str,
) -> Option<bool> {
    let reply = bus::call(
        context,
        desktop,
        Some(name),
        path,
        name,
        "GetActive",
        None,
        CALL_TIMEOUT,
    )
    .ok()?;
    if !reply.is_type(glib::VariantTy::new("(b)").ok()?) {
        return None;
    }
    reply.child_value(0).get::<bool>()
}

thread_local! {
    /// Monitors by key, so the bus close handler, which GObject requires to
    /// be thread-safe, carries only a key. It runs on the monitor's context.
    static CLOSE_TARGETS: RefCell<HashMap<u64, Weak<Inner>>> = RefCell::new(HashMap::new());
}

pub(super) fn watch_connection(
    connection: &gio::DBusConnection,
    monitor: &Weak<Inner>,
    system: bool,
) {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let key = NEXT.fetch_add(1, Ordering::Relaxed);
    CLOSE_TARGETS.with(|targets| targets.borrow_mut().insert(key, monitor.clone()));
    connection.connect_closed(move |_, _, _| {
        let Some(inner) = CLOSE_TARGETS
            .with(|targets| targets.borrow_mut().remove(&key))
            .and_then(|monitor| monitor.upgrade())
        else {
            return;
        };
        let mut next = inner.current();
        if system {
            next.system_connected = false;
            next.session = None;
            next.session_error = Some(session::SessionUnavailable::Unreadable);
        } else {
            next.session_bus_connected = false;
            next.screensaver_locked = None;
        }
        inner.lose(next);
    });
}

pub(super) fn watch_owner(
    buses: &mut Buses,
    connection: &gio::DBusConnection,
    name: &str,
    monitor: &Weak<Inner>,
) {
    let monitor = monitor.clone();
    let system = name == session::NAME;
    let id = connection.signal_subscribe(
        Some("org.freedesktop.DBus"),
        Some("org.freedesktop.DBus"),
        Some("NameOwnerChanged"),
        Some("/org/freedesktop/DBus"),
        Some(name),
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, _| {
            let Some(inner) = monitor.upgrade() else {
                return;
            };
            let mut next = inner.current();
            if system {
                next.session = None;
                next.session_error = Some(session::SessionUnavailable::Unreadable);
            } else {
                next.screensaver_locked = None;
            }
            inner.lose(next);
        },
    );
    buses.subscriptions.push((connection.clone(), id));
}

/// Subscribes one signal and hands each well-typed one to `on`.
fn subscribe(
    buses: &mut Buses,
    connection: &gio::DBusConnection,
    monitor: &Weak<Inner>,
    (sender, interface, member, path): (&str, &str, &str, &str),
    signature: &'static str,
    on: impl Fn(&Inner, &glib::Variant) + 'static,
) {
    let monitor = monitor.clone();
    let id = connection.signal_subscribe(
        Some(sender),
        Some(interface),
        Some(member),
        Some(path),
        None,
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            let Some(inner) = monitor.upgrade() else {
                return;
            };
            if glib::VariantTy::new(signature).is_ok_and(|ty| parameters.is_type(ty)) {
                on(&inner, parameters);
            }
        },
    );
    buses.subscriptions.push((connection.clone(), id));
}

pub(super) fn subscribe_system(
    buses: &mut Buses,
    connection: &gio::DBusConnection,
    monitor: &Weak<Inner>,
    path: Option<&str>,
) {
    watch_owner(buses, connection, session::NAME, monitor);
    let manager = (
        session::NAME,
        "org.freedesktop.login1.Manager",
        "PrepareForSleep",
        "/org/freedesktop/login1",
    );
    subscribe(
        buses,
        connection,
        monitor,
        manager,
        "(b)",
        |inner, parameters| {
            let mut next = inner.current();
            next.sleeping = parameters.child_value(0).get::<bool>().unwrap_or(true);
            if !next.sleeping {
                // A wake requests nothing; reread what changed while asleep.
                next.session = None;
                next.session_error = Some(session::SessionUnavailable::Unreadable);
                next.screensaver_locked = None;
                inner.refresh_requested.set(true);
            }
            inner.apply(next, LockReason::Sleep);
        },
    );
    let Some(path) = path else {
        return;
    };
    for (member, locked) in [("Lock", true), ("Unlock", false)] {
        let signal = (session::NAME, session::SESSION_INTERFACE, member, path);
        subscribe(buses, connection, monitor, signal, "()", move |inner, _| {
            let mut next = inner.current();
            if let Some(session) = &mut next.session {
                session.locked = locked;
            }
            inner.apply(next, LockReason::ScreenLocked);
        });
    }
    let changed = (
        session::NAME,
        "org.freedesktop.DBus.Properties",
        "PropertiesChanged",
        path,
    );
    subscribe(
        buses,
        connection,
        monitor,
        changed,
        "(sa{sv}as)",
        |inner, parameters| {
            if parameters.child_value(0).str() != Some(session::SESSION_INTERFACE) {
                return;
            }
            let changed = glib::VariantDict::new(Some(&parameters.child_value(1)));
            let mut next = inner.current();
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
                // The session's identity changed under us.
                next.session = None;
                next.session_error = Some(session::SessionUnavailable::Unreadable);
                inner.lose(next);
                return;
            }
            inner.apply(next, LockReason::ScreenLocked);
        },
    );
}

pub(super) fn subscribe_screensaver(
    buses: &mut Buses,
    connection: &gio::DBusConnection,
    monitor: &Weak<Inner>,
    name: &str,
    path: &str,
) {
    subscribe(
        buses,
        connection,
        monitor,
        (name, name, "ActiveChanged", path),
        "(b)",
        |inner, parameters| {
            let mut next = inner.current();
            next.screensaver_locked = parameters.child_value(0).get::<bool>();
            inner.apply(next, LockReason::ScreenLocked);
        },
    );
}
