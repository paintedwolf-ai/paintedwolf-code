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

const SESSION_PATH: &str = "/org/freedesktop/login1/session/fixture";
const LOGIN_PATH: &str = "/org/freedesktop/login1";
const MANAGER: &str = "org.freedesktop.login1.Manager";
const SCREENSAVER: &str = "org.freedesktop.ScreenSaver";
const SCREENSAVER_PATH: &str = "/org/freedesktop/ScreenSaver";

fn uid() -> u32 {
    // SAFETY: getuid has no preconditions and cannot fail.
    unsafe { libc::getuid() }
}

/// A private dbus-daemon listening on a fixed socket, so it can be stopped
/// and started again at the same address.
struct Daemon {
    child: Option<Child>,
    root: PathBuf,
    socket: PathBuf,
}

impl Daemon {
    fn new(name: &str) -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let root = std::env::temp_dir().join(format!(
            "pw-step-away-{name}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir(&root).unwrap();
        let socket = root.join("bus");
        let mut daemon = Self {
            child: None,
            root,
            socket,
        };
        daemon.start();
        daemon
    }

    fn address(&self) -> String {
        format!("unix:path={}", self.socket.display())
    }

    fn start(&mut self) {
        let _ = std::fs::remove_file(&self.socket);
        let config = self.root.join("bus.conf");
        // No service directories: nothing ambient can be activated here.
        std::fs::write(
            &config,
            format!(
                "<busconfig><type>session</type><listen>{}</listen><auth>EXTERNAL</auth>\
                 <policy context='default'><allow own='*'/><allow send_destination='*'/>\
                 <allow receive_sender='*'/></policy></busconfig>",
                self.address()
            ),
        )
        .unwrap();
        let mut child = Command::new("dbus-daemon")
            .arg(format!("--config-file={}", config.display()))
            .args(["--nofork", "--print-address=1"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("dbus-daemon is a Den Linux test dependency");
        let mut line = String::new();
        BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut line)
            .unwrap();
        assert!(!line.trim().is_empty(), "dbus-daemon printed no address");
        self.child = Some(child);
    }

    fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

impl Drop for Daemon {
    fn drop(&mut self) {
        self.stop();
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

/// What the fixture services answer.
#[derive(Clone)]
struct Facts {
    active: bool,
    locked: bool,
    sleeping: bool,
    screensaver: bool,
    session_uid: u32,
}

impl Default for Facts {
    fn default() -> Self {
        Self {
            active: true,
            locked: false,
            sleeping: false,
            screensaver: false,
            session_uid: uid(),
        }
    }
}

enum Request {
    Emit {
        path: &'static str,
        interface: &'static str,
        member: &'static str,
        parameters: glib::Variant,
    },
    Release(&'static str),
}

/// One fixture connection serving its objects on its own thread.
struct Service {
    requests: mpsc::Sender<Request>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

const LOGIND_XML: &str = "<node>\
<interface name='org.freedesktop.login1.Manager'>\
<method name='GetSessionByPID'><arg type='u' direction='in'/><arg type='o' direction='out'/></method>\
<method name='GetUser'><arg type='u' direction='in'/><arg type='o' direction='out'/></method>\
<property name='PreparingForSleep' type='b' access='read'/>\
<signal name='PrepareForSleep'><arg type='b'/></signal></interface>\
<interface name='org.freedesktop.login1.Session'>\
<property name='Id' type='s' access='read'/><property name='Type' type='s' access='read'/>\
<property name='Class' type='s' access='read'/><property name='User' type='(uo)' access='read'/>\
<property name='Active' type='b' access='read'/><property name='LockedHint' type='b' access='read'/>\
<signal name='Lock'/><signal name='Unlock'/></interface>\
<interface name='org.freedesktop.login1.User'><property name='Display' type='(so)' access='read'/></interface>\
</node>";

const SCREENSAVER_XML: &str = "<node><interface name='org.freedesktop.ScreenSaver'>\
<method name='GetActive'><arg type='b' direction='out'/></method>\
<signal name='ActiveChanged'><arg type='b'/></signal></interface></node>";

impl Service {
    fn logind(daemon: &Daemon, facts: Arc<Mutex<Facts>>) -> Self {
        Self::start(daemon.address(), facts, true)
    }

    fn screensaver(daemon: &Daemon, facts: Arc<Mutex<Facts>>) -> Self {
        Self::start(daemon.address(), facts, false)
    }

    fn start(address: String, facts: Arc<Mutex<Facts>>, logind: bool) -> Self {
        let stop = Arc::new(AtomicBool::new(false));
        let stopping = stop.clone();
        let (requests, incoming) = mpsc::channel::<Request>();
        let (ready, started) = mpsc::channel();
        let worker = thread::spawn(move || {
            let context = glib::MainContext::new();
            context
                .with_thread_default(|| {
                    let connection = gio::DBusConnection::for_address_sync(
                        &address,
                        gio::DBusConnectionFlags::AUTHENTICATION_CLIENT
                            | gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
                        None::<&gio::DBusAuthObserver>,
                        None::<&gio::Cancellable>,
                    )
                    .unwrap();
                    let registrations = if logind {
                        register_logind(&connection, &facts)
                    } else {
                        register_screensaver(&connection, &facts)
                    };
                    let name = if logind { session::NAME } else { SCREENSAVER };
                    request_name(&connection, name);
                    ready.send(()).unwrap();
                    while !stopping.load(Ordering::Acquire) {
                        while let Ok(request) = incoming.try_recv() {
                            match request {
                                Request::Emit {
                                    path,
                                    interface,
                                    member,
                                    parameters,
                                } => {
                                    connection
                                        .emit_signal(
                                            None,
                                            path,
                                            interface,
                                            member,
                                            Some(&parameters),
                                        )
                                        .unwrap();
                                    connection.flush_sync(None::<&gio::Cancellable>).unwrap();
                                }
                                Request::Release(name) => {
                                    let _ = connection.call_sync(
                                        Some("org.freedesktop.DBus"),
                                        "/org/freedesktop/DBus",
                                        "org.freedesktop.DBus",
                                        "ReleaseName",
                                        Some(&(name,).to_variant()),
                                        None,
                                        gio::DBusCallFlags::NONE,
                                        1500,
                                        None::<&gio::Cancellable>,
                                    );
                                }
                            }
                        }
                        context.iteration(false);
                        thread::sleep(Duration::from_millis(2));
                    }
                    for id in registrations {
                        let _ = connection.unregister_object(id);
                    }
                    let _ = connection.close_sync(None::<&gio::Cancellable>);
                })
                .unwrap();
        });
        started
            .recv_timeout(Duration::from_secs(5))
            .expect("fixture service started");
        Self {
            requests,
            stop,
            worker: Some(worker),
        }
    }

    fn emit(
        &self,
        path: &'static str,
        interface: &'static str,
        member: &'static str,
        parameters: glib::Variant,
    ) {
        self.requests
            .send(Request::Emit {
                path,
                interface,
                member,
                parameters,
            })
            .unwrap();
    }

    fn release(&self, name: &'static str) {
        self.requests.send(Request::Release(name)).unwrap();
    }
}

impl Drop for Service {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

fn request_name(connection: &gio::DBusConnection, name: &str) {
    connection
        .call_sync(
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "RequestName",
            Some(&(name, 0u32).to_variant()),
            None,
            gio::DBusCallFlags::NONE,
            1500,
            None::<&gio::Cancellable>,
        )
        .unwrap();
}

fn register_logind(
    connection: &gio::DBusConnection,
    facts: &Arc<Mutex<Facts>>,
) -> Vec<gio::RegistrationId> {
    let node = gio::DBusNodeInfo::for_xml(LOGIND_XML).unwrap();
    let user_path = format!("/org/freedesktop/login1/user/_{}", uid());
    let mut ids = Vec::new();
    for (path, interface) in [
        (LOGIN_PATH.to_string(), MANAGER),
        (SESSION_PATH.to_string(), session::SESSION_INTERFACE),
        (user_path.clone(), "org.freedesktop.login1.User"),
    ] {
        let info = node.lookup_interface(interface).unwrap();
        let reads = facts.clone();
        let user = user_path.clone();
        ids.push(
            connection
                .register_object(
                    &path,
                    &info,
                    move |_, _, _, _, method, _, invocation| match method {
                        "GetSessionByPID" | "GetUser" => {
                            let target = if method == "GetSessionByPID" {
                                SESSION_PATH
                            } else {
                                user.as_str()
                            };
                            invocation.return_value(Some(
                                &(ObjectPath::try_from(target).unwrap(),).to_variant(),
                            ));
                        }
                        _ => invocation
                            .return_dbus_error("org.freedesktop.DBus.Error.UnknownMethod", method),
                    },
                    move |_, _, _, _, property| {
                        let facts = reads.lock().unwrap().clone();
                        match property {
                            "PreparingForSleep" => facts.sleeping.to_variant(),
                            "Id" => "fixture".to_variant(),
                            "Type" => "wayland".to_variant(),
                            "Class" => "user".to_variant(),
                            "User" => (
                                facts.session_uid,
                                ObjectPath::try_from(format!(
                                    "/org/freedesktop/login1/user/_{}",
                                    facts.session_uid
                                ))
                                .unwrap(),
                            )
                                .to_variant(),
                            "Active" => facts.active.to_variant(),
                            "LockedHint" => facts.locked.to_variant(),
                            "Display" => ("fixture", ObjectPath::try_from(SESSION_PATH).unwrap())
                                .to_variant(),
                            _ => false.to_variant(),
                        }
                    },
                    |_, _, _, _, _, _| false,
                )
                .unwrap(),
        );
    }
    ids
}

fn register_screensaver(
    connection: &gio::DBusConnection,
    facts: &Arc<Mutex<Facts>>,
) -> Vec<gio::RegistrationId> {
    let node = gio::DBusNodeInfo::for_xml(SCREENSAVER_XML).unwrap();
    let info = node.lookup_interface(SCREENSAVER).unwrap();
    let reads = facts.clone();
    vec![connection
        .register_object(
            SCREENSAVER_PATH,
            &info,
            move |_, _, _, _, _, _, invocation| {
                let active = reads.lock().unwrap().screensaver;
                invocation.return_value(Some(&(active,).to_variant()));
            },
            |_, _, _, _, _| false.to_variant(),
            |_, _, _, _, _, _| false,
        )
        .unwrap()]
}

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
        fixture.logind().release(session::NAME);
        h.pump_until("owner loss", |h| !h.allowed());
        assert!(h.locks().contains(&LockReason::ScreenLocked));
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
