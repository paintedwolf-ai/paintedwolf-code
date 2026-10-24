//! The step-away monitor against private buses: one dbus-daemon stands in
//! for the system bus with a fixture logind, another for the session bus
//! with a fixture screen saver.

use super::*;
use std::collections::HashMap;
use std::io::{BufRead, BufReader};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc;
use std::thread;
use std::time::Instant;

use gtk::glib::variant::ObjectPath;
use gtk::glib::ToVariant;

mod buses;

use buses::*;

/// A monitor on its own context against the two daemons, recording every lock
/// it asks for.
struct Harness {
    context: glib::MainContext,
    monitor: Monitor,
    locks: Rc<RefCell<Vec<LockReason>>>,
}

impl Harness {
    fn new(system: &Daemon, desktop: &Daemon) -> Self {
        let context = glib::MainContext::new();
        let locks = Rc::new(RefCell::new(Vec::new()));
        let recorded = locks.clone();
        let monitor = Monitor::new(
            BusAddresses {
                system: system.address(),
                session: Ok(desktop.address()),
            },
            context.clone(),
            Box::new(move |reason| recorded.borrow_mut().push(reason)),
        );
        Self {
            context,
            monitor,
            locks,
        }
    }

    /// Runs `body` with this harness's context as the thread default, as the
    /// monitor's bus calls and signal dispatch require.
    fn run(&self, body: impl FnOnce(&Self)) {
        self.context.with_thread_default(|| body(self)).unwrap();
    }

    fn pump_until(&self, what: &str, done: impl Fn(&Self) -> bool) {
        let deadline = Instant::now() + Duration::from_secs(5);
        while !done(self) {
            assert!(
                Instant::now() < deadline,
                "timed out waiting for {what}: {:?}",
                self.monitor.status()
            );
            self.context.iteration(false);
            thread::sleep(Duration::from_millis(2));
        }
    }

    fn allowed(&self) -> bool {
        self.monitor.status().confirmation_allowed()
    }

    fn locks(&self) -> Vec<LockReason> {
        self.locks.borrow().clone()
    }
}

fn properties_changed(changed: &[(&str, glib::Variant)]) -> glib::Variant {
    let changed: HashMap<String, glib::Variant> = changed
        .iter()
        .map(|(key, value)| ((*key).to_string(), value.clone()))
        .collect();
    (session::SESSION_INTERFACE, changed, Vec::<String>::new()).to_variant()
}

struct Fixture {
    system: Daemon,
    desktop: Daemon,
    facts: Arc<Mutex<Facts>>,
    logind: Option<Service>,
    screensaver: Option<Service>,
}

impl Fixture {
    fn new(facts: Facts, screensaver: bool) -> Self {
        let system = Daemon::new("system");
        let desktop = Daemon::new("session");
        let facts = Arc::new(Mutex::new(facts));
        let logind = Some(Service::logind(&system, facts.clone()));
        let screensaver = screensaver.then(|| Service::screensaver(&desktop, facts.clone()));
        Self {
            system,
            desktop,
            facts,
            logind,
            screensaver,
        }
    }

    fn logind(&self) -> &Service {
        self.logind.as_ref().unwrap()
    }
}

#[test]
fn an_active_unlocked_session_confirms_and_reports_its_sources() {
    let fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        let status = h.monitor.status();
        assert!(
            status.system_connected && status.session_bus_connected,
            "{status:?}"
        );
        let session = status.session.as_ref().expect("fixture session read");
        assert!(session.active && !session.locked && !session.can_lock);
        assert_eq!(status.screensaver_locked, Some(false));
        assert!(status.logind_owner.as_deref().is_some_and(|o| o.starts_with(':')));
        assert!(status.screensaver_owner.as_deref().is_some_and(|o| o.starts_with(':')));
        assert!(h.allowed());
        assert!(h.locks().is_empty());
    });
}

#[test]
fn an_initially_locked_or_inactive_session_locks_and_cannot_confirm() {
    for facts in [
        Facts {
            locked: true,
            ..Facts::default()
        },
        Facts {
            active: false,
            ..Facts::default()
        },
    ] {
        let fixture = Fixture::new(facts, false);
        let harness = Harness::new(&fixture.system, &fixture.desktop);
        harness.run(|h| {
            h.monitor.refresh();
            assert!(!h.allowed());
            assert_eq!(h.locks(), vec![LockReason::ScreenLocked]);
        });
    }
}

#[test]
fn another_users_session_cannot_confirm() {
    let fixture = Fixture::new(
        Facts {
            session_uid: uid() + 1,
            ..Facts::default()
        },
        true,
    );
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        assert_eq!(
            h.monitor.status().session_error,
            Some(session::SessionUnavailable::WrongUser)
        );
        assert!(!h.allowed());
    });
}

#[test]
fn lock_and_unlock_signals_follow_the_session() {
    let fixture = Fixture::new(Facts::default(), false);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        assert!(h.allowed());
        fixture.logind().emit(
            SESSION_PATH,
            session::SESSION_INTERFACE,
            "Lock",
            ().to_variant(),
        );
        h.pump_until("the Lock signal", |h| !h.allowed());
        assert_eq!(h.locks(), vec![LockReason::ScreenLocked]);
        fixture.logind().emit(
            SESSION_PATH,
            session::SESSION_INTERFACE,
            "Unlock",
            ().to_variant(),
        );
        h.pump_until("the Unlock signal", Harness::allowed);
        assert_eq!(h.locks().len(), 1, "an unlock requests nothing");
    });
}

#[test]
fn locked_hint_and_activity_changes_lock() {
    let fixture = Fixture::new(Facts::default(), false);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        let changed = properties_changed(&[("LockedHint", true.to_variant())]);
        fixture.logind().emit(
            SESSION_PATH,
            "org.freedesktop.DBus.Properties",
            "PropertiesChanged",
            changed,
        );
        h.pump_until("LockedHint", |h| !h.allowed());
        let changed = properties_changed(&[("LockedHint", false.to_variant())]);
        fixture.logind().emit(
            SESSION_PATH,
            "org.freedesktop.DBus.Properties",
            "PropertiesChanged",
            changed,
        );
        h.pump_until("LockedHint cleared", Harness::allowed);
        let changed = properties_changed(&[("Active", false.to_variant())]);
        fixture.logind().emit(
            SESSION_PATH,
            "org.freedesktop.DBus.Properties",
            "PropertiesChanged",
            changed,
        );
        h.pump_until("an inactive session", |h| !h.allowed());
        assert_eq!(
            h.locks(),
            vec![LockReason::ScreenLocked, LockReason::ScreenLocked]
        );
    });
}

#[test]
fn a_changed_session_identity_is_stale_until_reread() {
    let fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        let changed = properties_changed(&[(
            "User",
            (uid(), ObjectPath::try_from("/x").unwrap()).to_variant(),
        )]);
        fixture.logind().emit(
            SESSION_PATH,
            "org.freedesktop.DBus.Properties",
            "PropertiesChanged",
            changed,
        );
        h.pump_until("identity change", |h| !h.allowed());
        assert!(h.locks().contains(&LockReason::ScreenLocked));
        h.monitor.tick();
        assert!(h.allowed(), "the refresh rereads the session");
    });
}

#[test]
fn sleep_locks_and_a_wake_rereads_before_confirming() {
    let fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        fixture
            .logind()
            .emit(LOGIN_PATH, MANAGER, "PrepareForSleep", (true,).to_variant());
        h.pump_until("sleep", |h| !h.allowed());
        assert_eq!(h.locks(), vec![LockReason::Sleep]);
        fixture.logind().emit(
            LOGIN_PATH,
            MANAGER,
            "PrepareForSleep",
            (false,).to_variant(),
        );
        h.pump_until("wake", |h| !h.monitor.status().sleeping);
        assert!(
            !h.allowed(),
            "a wake confirms nothing until the sources are reread"
        );
        h.monitor.tick();
        assert!(h.allowed());
    });
}

#[test]
fn the_screen_saver_locks_when_it_activates() {
    let fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        let service = fixture.screensaver.as_ref().unwrap();
        service.emit(
            SCREENSAVER_PATH,
            SCREENSAVER,
            "ActiveChanged",
            (true,).to_variant(),
        );
        h.pump_until("screen saver", |h| !h.allowed());
        assert_eq!(h.locks(), vec![LockReason::ScreenLocked]);
        service.emit(
            SCREENSAVER_PATH,
            SCREENSAVER,
            "ActiveChanged",
            (false,).to_variant(),
        );
        h.pump_until("screen saver off", Harness::allowed);
    });
}

#[test]
fn losing_the_logind_owner_locks_and_recovers_on_refresh() {
    let mut fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        let initial_owner = h.monitor.status().logind_owner.clone();
        assert!(initial_owner.as_deref().is_some_and(|o| o.starts_with(':')));
        fixture.logind().release(session::NAME);
        h.pump_until("owner loss", |h| !h.allowed());
        assert!(h.locks().contains(&LockReason::ScreenLocked));
        assert_eq!(h.monitor.status().logind_owner, None);
        assert_eq!(
            h.monitor.status().session_error,
            Some(session::SessionUnavailable::Unreadable)
        );
        // The screen saver alone does not stand in for a stale logind session.
        h.monitor.tick();
        assert!(!h.allowed(), "no owner answers the reread");
        fixture.logind = None;
        fixture.logind = Some(Service::logind(&fixture.system, fixture.facts.clone()));
        h.pump_until("the returned owner reread", |h| {
            h.monitor.tick();
            h.allowed()
        });
        let recovered_owner = h.monitor.status().logind_owner;
        assert!(recovered_owner.as_deref().is_some_and(|o| o.starts_with(':')));
        assert_ne!(recovered_owner, initial_owner);
    });
}

#[test]
fn a_closed_system_bus_locks_and_reconnects_when_it_returns() {
    let mut fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        assert!(h.allowed());
        fixture.logind = None;
        fixture.system.stop();
        h.pump_until("bus loss", |h| !h.monitor.status().system_connected);
        assert!(!h.allowed());
        assert!(h.locks().contains(&LockReason::ScreenLocked));
        h.monitor.tick();
        assert!(!h.allowed(), "no system bus answers");
        fixture.system.start();
        fixture.logind = Some(Service::logind(&fixture.system, fixture.facts.clone()));
        h.monitor.tick();
        let status = h.monitor.status();
        assert!(
            status.system_connected && status.session.is_some(),
            "{status:?}"
        );
        assert!(h.allowed());
    });
}

#[test]
fn a_closed_session_bus_drops_the_screen_saver_source() {
    let mut fixture = Fixture::new(Facts::default(), true);
    let harness = Harness::new(&fixture.system, &fixture.desktop);
    harness.run(|h| {
        h.monitor.refresh();
        fixture.screensaver = None;
        fixture.desktop.stop();
        h.pump_until("session bus loss", |h| {
            !h.monitor.status().session_bus_connected
        });
        assert_eq!(h.monitor.status().screensaver_locked, None);
        assert!(h.locks().contains(&LockReason::ScreenLocked));
    });
}
