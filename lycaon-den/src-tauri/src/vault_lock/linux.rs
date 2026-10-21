//! Initial lock facts, native signals and bounded bus reconnection.

use std::cell::{Cell, RefCell};
use std::rc::{Rc, Weak};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Duration;

use gtk::glib::ToVariant;
use gtk::{gio, glib};
use tauri::AppHandle;

use super::{lock_in_background, LockReason};
use crate::linux_desktop::{bus, session};

mod state;
pub(crate) use state::LockSources;

#[cfg(test)]
mod tests;

const CONNECT_TIMEOUT: Duration = Duration::from_millis(1500);
const CALL_TIMEOUT: Duration = Duration::from_secs(2);
const RECONNECT_INTERVAL: u32 = 5;

const SCREENSAVERS: [(&str, &str); 2] = [
    (
        "org.freedesktop.ScreenSaver",
        "/org/freedesktop/ScreenSaver",
    ),
    ("org.gnome.ScreenSaver", "/org/gnome/ScreenSaver"),
];

/// The monitor that serves `vault_lock_status` and presence confirmation.
static OBSERVED: OnceLock<Arc<Mutex<LockSources>>> = OnceLock::new();

pub(crate) fn status() -> LockSources {
    OBSERVED
        .get()
        .map(|sources| sources.lock().unwrap().clone())
        .unwrap_or_default()
}

pub(crate) fn confirmation_allowed() -> bool {
    status().confirmation_allowed()
}

/// Where the monitor reaches its two buses. Production uses the fixed system
/// socket, never `DBUS_SYSTEM_BUS_ADDRESS`, and the person's session bus.
pub(super) struct BusAddresses {
    pub system: String,
    pub session: Result<String, String>,
}

impl BusAddresses {
    fn native() -> Self {
        Self {
            system: bus::SYSTEM_BUS_ADDRESS.to_string(),
            session: bus::session_address(),
        }
    }
}

pub(super) fn setup_linux(app: AppHandle) -> Result<(), String> {
    thread_local! { static INSTALLED: RefCell<Option<Monitor>> = const { RefCell::new(None) }; }
    if INSTALLED.with(|installed| installed.borrow().is_some()) {
        return Ok(());
    }
    let monitor = Monitor::new(
        BusAddresses::native(),
        glib::MainContext::default(),
        Box::new(move |reason| lock_in_background(&app, reason)),
    );
    let _ = OBSERVED.set(monitor.sources());
    monitor.refresh();
    let ticking = monitor.clone();
    glib::timeout_add_seconds_local(RECONNECT_INTERVAL, move || {
        ticking.tick();
        glib::ControlFlow::Continue
    });
    INSTALLED.with(|installed| *installed.borrow_mut() = Some(monitor));
    Ok(())
}

/// Observes logind and the desktop's screen saver on one glib context and
/// asks its sink to lock when the person steps away. Main-context only.
#[derive(Clone)]
pub(super) struct Monitor {
    inner: Rc<Inner>,
}

struct Inner {
    addresses: BusAddresses,
    context: glib::MainContext,
    sink: Box<dyn Fn(LockReason)>,
    sources: Arc<Mutex<LockSources>>,
    refresh_requested: Cell<bool>,
    refreshing: Cell<bool>,
    buses: RefCell<Buses>,
}

#[derive(Default)]
struct Buses {
    system: Option<gio::DBusConnection>,
    desktop: Option<gio::DBusConnection>,
    subscriptions: Vec<(gio::DBusConnection, gio::SignalSubscriptionId)>,
}

impl Monitor {
    pub(super) fn new(
        addresses: BusAddresses,
        context: glib::MainContext,
        sink: Box<dyn Fn(LockReason)>,
    ) -> Self {
        Self {
            inner: Rc::new(Inner {
                addresses,
                context,
                sink,
                sources: Arc::new(Mutex::new(LockSources::default())),
                refresh_requested: Cell::new(false),
                refreshing: Cell::new(false),
                buses: RefCell::new(Buses::default()),
            }),
        }
    }

    pub(super) fn sources(&self) -> Arc<Mutex<LockSources>> {
        self.inner.sources.clone()
    }

    pub(super) fn status(&self) -> LockSources {
        self.inner.sources.lock().unwrap().clone()
    }

    /// Reconnects a closed bus, or rereads every source after an event that
    /// left the observations stale.
    pub(super) fn tick(&self) {
        if self.inner.refreshing.get() {
            return;
        }
        let reconnect = {
            let buses = self.inner.buses.borrow();
            buses.system.as_ref().is_none_or(|bus| bus.is_closed())
                || buses.desktop.as_ref().is_none_or(|bus| bus.is_closed())
        };
        if reconnect || self.inner.refresh_requested.replace(false) {
            self.refresh();
        }
    }

    /// Rereads the initial facts and resubscribes.
    pub(super) fn refresh(&self) {
        if self.inner.refreshing.replace(true) {
            self.inner.refresh_requested.set(true);
            return;
        }
        let next = self.read_sources();
        self.inner.refreshing.set(false);
        self.inner.apply(next, LockReason::ScreenLocked);
    }

    fn read_sources(&self) -> LockSources {
        let inner = &self.inner;
        let weak = Rc::downgrade(inner);
        let context = &inner.context;
        let mut guard = inner.buses.borrow_mut();
        let buses = &mut *guard;
        for (connection, id) in buses.subscriptions.drain(..) {
            connection.signal_unsubscribe(id);
        }
        if buses.system.as_ref().is_none_or(|bus| bus.is_closed()) {
            buses.system = bus::connect(context, &inner.addresses.system, CONNECT_TIMEOUT).ok();
            if let Some(system) = &buses.system {
                watch_connection(system, &weak, true);
            }
        }
        if buses.desktop.as_ref().is_none_or(|bus| bus.is_closed()) {
            buses.desktop = inner
                .addresses
                .session
                .as_ref()
                .ok()
                .and_then(|address| bus::connect(context, address, CONNECT_TIMEOUT).ok());
            if let Some(desktop) = &buses.desktop {
                watch_connection(desktop, &weak, false);
            }
        }
        let mut current = LockSources {
            system_connected: buses.system.is_some(),
            session_bus_connected: buses.desktop.is_some(),
            ..LockSources::default()
        };
        if let Some(system) = buses.system.clone() {
            current.sleeping = preparing_for_sleep(context, &system).unwrap_or(false);
            match session::discover(context, &system) {
                Ok(session) => current.session = Some(session),
                Err(error) => current.session_error = Some(error),
            }
            let path = current.session.as_ref().map(|session| session.path.clone());
            subscribe_system(buses, &system, &weak, path.as_deref());
        }
        if let Some(desktop) = buses.desktop.clone() {
            for (name, path) in SCREENSAVERS {
                if let Some(active) = screensaver_active(context, &desktop, name, path) {
                    current.screensaver_locked = Some(active);
                    subscribe_screensaver(buses, &desktop, &weak, name, path);
                    break;
                }
            }
            for (name, _) in SCREENSAVERS {
                watch_owner(buses, &desktop, name, &weak);
            }
        }
        current
    }
}

impl Inner {
    fn current(&self) -> LockSources {
        self.sources.lock().unwrap().clone()
    }

    /// Publishes new observations and locks when confirmation stopped being
    /// allowed or a lock was observed.
    fn apply(&self, next: LockSources, reason: LockReason) {
        let mut sources = self.sources.lock().unwrap();
        let was_allowed = sources.confirmation_allowed();
        let now_allowed = next.confirmation_allowed();
        let locked = next.observed_away();
        *sources = next;
        eprintln!(
            "Step-away capability facts: {}",
            serde_json::to_string(&*sources).unwrap_or_default()
        );
        drop(sources);
        if !now_allowed && (was_allowed || locked) {
            (self.sink)(reason);
        }
    }

    /// A source went away: its observations are stale, so lock, and reread
    /// everything on the next tick.
    fn lose(&self, next: LockSources) {
        self.apply(next, LockReason::ScreenLocked);
        (self.sink)(LockReason::ScreenLocked);
        self.refresh_requested.set(true);
    }
}

fn preparing_for_sleep(context: &glib::MainContext, system: &gio::DBusConnection) -> Option<bool> {
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

fn screensaver_active(
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

fn watch_connection(connection: &gio::DBusConnection, monitor: &Weak<Inner>, system: bool) {
    let monitor = monitor.clone();
    connection.connect_closed(move |_, _, _| {
        let Some(inner) = monitor.upgrade() else {
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

fn watch_owner(
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

fn subscribe_system(
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

fn subscribe_screensaver(
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
