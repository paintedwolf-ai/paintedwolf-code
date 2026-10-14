use super::*;
use std::{
    io::{BufRead, BufReader, Read},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, AtomicUsize, Ordering},
        mpsc, Arc, Mutex,
    },
    thread,
    time::Duration,
};

const INTERFACE: &str = r#"<node><interface name="org.freedesktop.Notifications">
<method name="GetCapabilities"><arg type="as" direction="out"/></method>
<method name="Notify"><arg type="s" direction="in"/><arg type="u" direction="in"/><arg type="s" direction="in"/><arg type="s" direction="in"/><arg type="s" direction="in"/><arg type="as" direction="in"/><arg type="a{sv}" direction="in"/><arg type="i" direction="in"/><arg type="u" direction="out"/></method>
<method name="CloseNotification"><arg type="u" direction="in"/></method>
<property name="Inhibited" type="b" access="read"/>
</interface></node>"#;

/// How the fixture service is reachable when the adapter connects.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Service {
    /// The fixture owns the well-known name before the adapter connects.
    Owned,
    /// Only a service file exists; the fixture takes the name on `own_name`.
    Activatable,
    /// Neither an owner nor a service file exists.
    Absent,
}

struct Fixture {
    _daemon: BusProcess,
    address: String,
    server: gio::DBusConnection,
    stopped: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
    requests: Arc<Mutex<Vec<(u32, String)>>>,
    closed: Arc<Mutex<Vec<u32>>>,
    inhibited: Arc<AtomicBool>,
}

struct BusProcess {
    child: Child,
    config_dir: PathBuf,
}

impl Drop for BusProcess {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = std::fs::remove_dir_all(&self.config_dir);
    }
}

static FIXTURES: AtomicUsize = AtomicUsize::new(0);

/// A private bus whose only activatable services are the fixture's own.
fn start_bus(service: Service) -> (BusProcess, String) {
    let config_dir = std::env::temp_dir().join(format!(
        "painted-wolf-notification-fixture-{}-{}",
        std::process::id(),
        FIXTURES.fetch_add(1, Ordering::Relaxed)
    ));
    let services = config_dir.join("services");
    std::fs::create_dir_all(&services).unwrap();
    if service == Service::Activatable {
        std::fs::write(
            services.join(format!("{SERVICE}.service")),
            format!("[D-BUS Service]\nName={SERVICE}\nExec=/bin/sleep 3\n"),
        )
        .unwrap();
    }
    let config = config_dir.join("session.conf");
    std::fs::write(
        &config,
        format!(
            r#"<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir={tmp}</listen>
  <servicedir>{services}</servicedir>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
"#,
            tmp = std::env::temp_dir().display(),
            services = services.display(),
        ),
    )
    .unwrap();
    let mut daemon = BusProcess {
        child: Command::new("dbus-daemon")
            .arg(format!("--config-file={}", config.display()))
            .args(["--nofork", "--print-address=1"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("start private session bus"),
        config_dir,
    };
    let output = daemon.child.stdout.take().unwrap();
    let (address_ready, address_receive) = mpsc::sync_channel(1);
    thread::spawn(move || {
        let mut address = String::new();
        let result = BufReader::new(output)
            .take(4096)
            .read_line(&mut address)
            .map(|_| address);
        let _ = address_ready.send(result);
    });
    let address = address_receive
        .recv_timeout(Duration::from_secs(2))
        .unwrap()
        .unwrap()
        .trim_end()
        .to_string();
    (daemon, address)
}

fn connect(context: &glib::MainContext, address: &str) -> gio::DBusConnection {
    context
        .block_on(glib::future_with_timeout(
            Duration::from_secs(2),
            gio::DBusConnection::for_address_future(
                address,
                gio::DBusConnectionFlags::AUTHENTICATION_CLIENT
                    | gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
                None,
            ),
        ))
        .unwrap()
        .unwrap()
}

fn request_name(connection: &gio::DBusConnection) {
    connection
        .call_sync(
            Some(BUS),
            BUS_PATH,
            BUS,
            "RequestName",
            Some(&(SERVICE, 0u32).to_variant()),
            None,
            gio::DBusCallFlags::NONE,
            500,
            gio::Cancellable::NONE,
        )
        .unwrap();
}

impl Fixture {
    fn new(capabilities: Vec<String>) -> Self {
        Self::start(capabilities, Service::Owned)
    }

    fn start(capabilities: Vec<String>, service: Service) -> Self {
        let (daemon, address) = start_bus(service);
        let stopped = Arc::new(AtomicBool::new(false));
        let stop = stopped.clone();
        let requests = Arc::new(Mutex::new(Vec::new()));
        let recorded = requests.clone();
        let closed = Arc::new(Mutex::new(Vec::new()));
        let canceled = closed.clone();
        let inhibited = Arc::new(AtomicBool::new(false));
        let paused = inhibited.clone();
        let server_address = address.clone();
        let (ready, receive) = mpsc::sync_channel(1);
        let worker = thread::spawn(move || {
            let context = glib::MainContext::new();
            context
                .with_thread_default(|| {
                    let connection = connect(&context, &server_address);
                    let node = gio::DBusNodeInfo::for_xml(INTERFACE).unwrap();
                    let interface = node.lookup_interface(SERVICE).unwrap();
                    let registration = connection
                        .register_object(
                            PATH,
                            &interface,
                            move |connection, _, _, _, method, parameters, invocation| match method
                            {
                                "GetCapabilities" => invocation
                                    .return_value(Some(&(capabilities.clone(),).to_variant())),
                                "Notify" => {
                                    let hints = parameters
                                        .child_value(6)
                                        .get::<HashMap<String, glib::Variant>>()
                                        .unwrap();
                                    assert_eq!(
                                        hints["desktop-entry"].get::<String>(),
                                        Some(desktop_entry())
                                    );
                                    let replacement =
                                        parameters.child_value(1).get::<u32>().unwrap();
                                    let body = parameters.child_value(4).get::<String>().unwrap();
                                    let mut recorded = recorded.lock().unwrap();
                                    recorded.push((replacement, body));
                                    let id = if replacement == 0 {
                                        recorded.len() as u32
                                    } else {
                                        replacement
                                    };
                                    invocation.return_value(Some(&(id,).to_variant()));
                                }
                                "CloseNotification" => {
                                    let (id,) = parameters.get::<(u32,)>().unwrap();
                                    canceled.lock().unwrap().push(id);
                                    invocation.return_value(Some(&().to_variant()));
                                    connection
                                        .emit_signal(
                                            None,
                                            PATH,
                                            SERVICE,
                                            "NotificationClosed",
                                            Some(&(id, 3u32).to_variant()),
                                        )
                                        .unwrap();
                                }
                                _ => invocation.return_dbus_error(
                                    "org.freedesktop.DBus.Error.UnknownMethod",
                                    "Unknown fixture method",
                                ),
                            },
                            move |_, _, _, _, _| paused.load(Ordering::Acquire).to_variant(),
                            |_, _, _, _, _, _| false,
                        )
                        .unwrap();
                    if service == Service::Owned {
                        request_name(&connection);
                    }
                    ready.send(connection.clone()).unwrap();
                    while !stop.load(Ordering::Acquire) {
                        for _ in 0..64 {
                            if !context.pending() {
                                break;
                            }
                            context.iteration(false);
                        }
                        thread::sleep(Duration::from_millis(2));
                    }
                    connection.unregister_object(registration).unwrap();
                    let _ = context.block_on(glib::future_with_timeout(
                        Duration::from_millis(500),
                        connection.close_future(),
                    ));
                })
                .unwrap();
        });
        let server = receive.recv_timeout(Duration::from_secs(3)).unwrap();
        Self {
            _daemon: daemon,
            address,
            server,
            stopped,
            worker: Some(worker),
            requests,
            closed,
            inhibited,
        }
    }

    fn signal(&self, signal: &str, parameters: glib::Variant) {
        self.server
            .emit_signal(None, PATH, SERVICE, signal, Some(&parameters))
            .unwrap();
        self.server.flush_sync(gio::Cancellable::NONE).unwrap();
    }

    /// Takes the well-known name after `delay`, as a service does once the bus
    /// has launched it.
    fn own_name_after(&self, delay: Duration) -> thread::JoinHandle<()> {
        let server = self.server.clone();
        thread::spawn(move || {
            thread::sleep(delay);
            request_name(&server);
        })
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        self.stopped.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            let result = worker.join();
            if !thread::panicking() {
                result.unwrap();
            }
        }
    }
}

fn pump(context: &glib::MainContext, predicate: impl Fn() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(2);
    while !predicate() {
        assert!(
            Instant::now() < deadline,
            "native notification signal did not arrive"
        );
        for _ in 0..64 {
            if !context.pending() {
                break;
            }
            context.iteration(false);
        }
        thread::sleep(Duration::from_millis(2));
    }
}

fn settle(context: &glib::MainContext) {
    for _ in 0..100 {
        while context.pending() {
            context.iteration(false);
        }
        thread::sleep(Duration::from_millis(2));
    }
}

/// The application identifier the shell passes as the desktop entry.
fn desktop_entry() -> String {
    let config: serde_json::Value =
        serde_json::from_str(include_str!("../../../tauri.conf.json")).unwrap();
    config["identifier"].as_str().unwrap().to_owned()
}

fn deadline() -> Instant {
    Instant::now() + Duration::from_secs(2)
}

#[test]
fn native_service_replaces_cancels_and_routes_only_owned_notifications() {
    let fixture = Fixture::new(vec!["actions".into(), "body".into(), "body-markup".into()]);
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let connection = connect(&context, &fixture.address);
            let activations = Rc::new(RefCell::new(Vec::new()));
            let routed = activations.clone();
            let delivery = Delivery::new(
                connection,
                desktop_entry(),
                move |session, token| routed.borrow_mut().push((session, token)),
                deadline(),
            )
            .unwrap();
            assert!(delivery.current(deadline()));
            assert_eq!(
                delivery
                    .send(
                        "Title".into(),
                        "<private> & text".into(),
                        Some("chat-a".into()),
                        deadline(),
                    )
                    .unwrap(),
                Outcome::Delivered
            );
            fixture.inhibited.store(true, Ordering::Release);
            assert_eq!(
                delivery
                    .send(
                        "Title".into(),
                        "Replacement".into(),
                        Some("chat-a".into()),
                        deadline(),
                    )
                    .unwrap(),
                Outcome::Inhibited
            );
            fixture.inhibited.store(false, Ordering::Release);
            assert_eq!(
                *fixture.requests.lock().unwrap(),
                vec![
                    (0, "&lt;private&gt; &amp; text".into()),
                    (1, "Replacement".into())
                ]
            );
            fixture.signal("ActivationToken", (1u32, "native-token").to_variant());
            fixture.signal("ActionInvoked", (99u32, "default").to_variant());
            fixture.signal("ActionInvoked", (1u32, "foreign").to_variant());
            fixture.signal("ActionInvoked", (1u32, "default").to_variant());
            pump(&context, || !activations.borrow().is_empty());
            assert_eq!(
                *activations.borrow(),
                vec![(Some("chat-a".into()), Some("native-token".into()))]
            );
            delivery
                .send(
                    "Title".into(),
                    "Chat B".into(),
                    Some("chat-b".into()),
                    deadline(),
                )
                .unwrap();
            delivery.cancel(&["chat-b".into()], deadline()).unwrap();
            assert_eq!(*fixture.closed.lock().unwrap(), vec![3]);
            fixture.signal("ActionInvoked", (3u32, "default").to_variant());
            delivery
                .send(
                    "Title".into(),
                    "Chat C".into(),
                    Some("chat-c".into()),
                    deadline(),
                )
                .unwrap();
            let foreign = connect(&context, &fixture.address);
            foreign
                .emit_signal(
                    None,
                    PATH,
                    SERVICE,
                    "ActionInvoked",
                    Some(&(4u32, "default").to_variant()),
                )
                .unwrap();
            settle(&context);
            assert_eq!(activations.borrow().len(), 1);
            assert!(delivery
                .send(
                    "Title".into(),
                    "Body".into(),
                    Some("invalid\nchat".into()),
                    deadline()
                )
                .is_err());
            assert!(delivery
                .send("Title".into(), "x".repeat((32 << 10) + 1), None, deadline())
                .is_err());
        })
        .unwrap();
}

#[test]
fn native_capability_admission_rejects_unbounded_and_disconnected_services() {
    for capabilities in [vec!["actions".into(); 65], vec!["x".repeat(129)]] {
        let fixture = Fixture::new(capabilities);
        let context = glib::MainContext::new();
        context
            .with_thread_default(|| {
                assert!(Delivery::new(
                    connect(&context, &fixture.address),
                    desktop_entry(),
                    |_, _| {},
                    deadline()
                )
                .is_err());
            })
            .unwrap();
    }
    let fixture = Fixture::new(vec!["actions".into()]);
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let delivery = Delivery::new(
                connect(&context, &fixture.address),
                desktop_entry(),
                |_, _| {},
                deadline(),
            )
            .unwrap();
            fixture
                .server
                .call_sync(
                    Some(BUS),
                    BUS_PATH,
                    BUS,
                    "ReleaseName",
                    Some(&(SERVICE,).to_variant()),
                    None,
                    gio::DBusCallFlags::NONE,
                    500,
                    gio::Cancellable::NONE,
                )
                .unwrap();
            assert!(!delivery.current(deadline()));
        })
        .unwrap();
}

#[test]
fn native_service_without_actions_preserves_delivery_and_clearing() {
    let fixture = Fixture::new(vec!["body".into()]);
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let activations = Rc::new(RefCell::new(0));
            let routed = activations.clone();
            let delivery = Delivery::new(
                connect(&context, &fixture.address),
                desktop_entry(),
                move |_, _| *routed.borrow_mut() += 1,
                deadline(),
            )
            .unwrap();
            delivery
                .send(
                    "Title".into(),
                    "Body".into(),
                    Some("chat-a".into()),
                    deadline(),
                )
                .unwrap();
            assert_eq!(*fixture.requests.lock().unwrap(), vec![(0, "Body".into())]);
            fixture.signal("ActionInvoked", (1u32, "default").to_variant());
            settle(&context);
            assert_eq!(*activations.borrow(), 0);
            delivery.cancel(&["chat-a".into()], deadline()).unwrap();
            assert_eq!(*fixture.closed.lock().unwrap(), vec![1]);
        })
        .unwrap();
}

#[test]
fn native_service_starts_from_its_service_file_or_reports_absence() {
    let absent = Fixture::start(vec!["body".into()], Service::Absent);
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let error = Delivery::new(
                connect(&context, &absent.address),
                desktop_entry(),
                |_, _| {},
                deadline(),
            )
            .err()
            .unwrap();
            assert!(error.contains("No desktop notification service is installed"));
        })
        .unwrap();
    drop(absent);

    let fixture = Fixture::start(vec!["body".into()], Service::Activatable);
    let owned = fixture.own_name_after(Duration::from_millis(300));
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let delivery = Delivery::new(
                connect(&context, &fixture.address),
                desktop_entry(),
                |_, _| {},
                deadline(),
            )
            .unwrap();
            assert!(delivery.current(deadline()));
            assert_eq!(
                delivery
                    .send("Title".into(), "Body".into(), None, deadline())
                    .unwrap(),
                Outcome::Delivered
            );
        })
        .unwrap();
    owned.join().unwrap();
    assert_eq!(*fixture.requests.lock().unwrap(), vec![(0, "Body".into())]);
}
