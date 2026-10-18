use super::*;
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::mpsc;
use std::thread;

const INTERFACES: &str = r#"<node>
<interface name="org.freedesktop.portal.OpenURI">
<method name="OpenURI"><arg type="s" direction="in"/><arg type="s" direction="in"/><arg type="a{sv}" direction="in"/><arg type="o" direction="out"/></method>
<method name="OpenFile"><arg type="s" direction="in"/><arg type="h" direction="in"/><arg type="a{sv}" direction="in"/><arg type="o" direction="out"/></method>
<method name="OpenDirectory"><arg type="s" direction="in"/><arg type="h" direction="in"/><arg type="a{sv}" direction="in"/><arg type="o" direction="out"/></method>
</interface>
<interface name="org.freedesktop.portal.Request"><method name="Close"/></interface>
</node>"#;

#[derive(Clone, Copy)]
enum Behavior {
    Respond(u32, bool, bool),
    Never,
    LoseOwner,
    Unsupported,
}

struct Daemon(Child);
impl Drop for Daemon {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

struct Fixture {
    _daemon: Daemon,
    address: String,
    stopped: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
    closed: Arc<AtomicUsize>,
    methods: Arc<Mutex<Vec<String>>>,
}

fn connect(context: &glib::MainContext, address: &str) -> gio::DBusConnection {
    crate::linux_desktop::bus::connect(context, address, Duration::from_secs(2)).unwrap()
}

impl Fixture {
    fn new(behavior: Behavior) -> Self {
        let mut daemon = Daemon(
            Command::new("dbus-daemon")
                .args(["--session", "--nofork", "--print-address=1"])
                .stdout(Stdio::piped())
                .stderr(Stdio::inherit())
                .spawn()
                .expect("start required private portal fixture bus"),
        );
        let output = daemon.0.stdout.take().unwrap();
        let (sender, receiver) = mpsc::sync_channel(1);
        thread::spawn(move || {
            let mut line = String::new();
            let _ = BufReader::new(output).take(4096).read_line(&mut line);
            let _ = sender.send(line.trim_end().to_string());
        });
        let address = receiver.recv_timeout(Duration::from_secs(2)).unwrap();
        assert!(
            address.starts_with("unix:"),
            "private bus address: {address}"
        );
        let server_address = address.clone();
        let stopped = Arc::new(AtomicBool::new(false));
        let stop = stopped.clone();
        let closed = Arc::new(AtomicUsize::new(0));
        let closes = closed.clone();
        let methods = Arc::new(Mutex::new(Vec::new()));
        let calls = methods.clone();
        let (ready, receiver) = mpsc::sync_channel(1);
        let worker = thread::spawn(move || {
            let context = glib::MainContext::new();
            context
                .with_thread_default(|| {
                    let bus = connect(&context, &server_address);
                    let node = gio::DBusNodeInfo::for_xml(INTERFACES).unwrap();
                    let request_interface = node.lookup_interface(REQUEST_INTERFACE).unwrap();
                    let registration = bus
                        .register_object(
                            PORTAL_OBJECT_PATH,
                            &node.lookup_interface(OPEN_URI_INTERFACE).unwrap(),
                            move |connection, sender, _, _, method, parameters, invocation| {
                                calls.lock().unwrap().push(method.to_string());
                                if matches!(behavior, Behavior::Unsupported) {
                                    invocation.return_dbus_error(
                                        "org.freedesktop.DBus.Error.UnknownMethod",
                                        "unsupported",
                                    );
                                    return;
                                }
                                let options = parameters
                                    .child_value(2)
                                    .get::<HashMap<String, glib::Variant>>()
                                    .unwrap();
                                let token = options["handle_token"].get::<String>().unwrap();
                                assert!(token.starts_with("paintedwolf_"));
                                let sender = sender.trim_start_matches(':').replace('.', "_");
                                let alternate = matches!(behavior, Behavior::Respond(_, _, true));
                                let token = if alternate {
                                    format!("alternate_{}", uuid::Uuid::new_v4().simple())
                                } else {
                                    token
                                };
                                let path = format!(
                                    "/org/freedesktop/portal/desktop/request/{sender}/{token}"
                                );
                                let closed = closes.clone();
                                connection
                                    .register_object(
                                        &path,
                                        &request_interface,
                                        move |_, _, _, _, method, _, invocation| {
                                            assert_eq!(method, "Close");
                                            closed.fetch_add(1, Ordering::SeqCst);
                                            invocation.return_value(Some(&().to_variant()));
                                        },
                                        |_, _, _, _, _| false.to_variant(),
                                        |_, _, _, _, _, _| false,
                                    )
                                    .unwrap();
                                let handle =
                                    glib::variant::ObjectPath::try_from(path.as_str()).unwrap();
                                let emit = || {
                                    let code = if let Behavior::Respond(code, _, _) = behavior {
                                        code
                                    } else {
                                        return;
                                    };
                                    connection
                                        .emit_signal(
                                            None,
                                            &path,
                                            REQUEST_INTERFACE,
                                            "Response",
                                            Some(
                                                &(code, HashMap::<String, glib::Variant>::new())
                                                    .to_variant(),
                                            ),
                                        )
                                        .unwrap();
                                    connection.flush_sync(gio::Cancellable::NONE).unwrap();
                                };
                                if matches!(behavior, Behavior::Respond(_, true, _)) {
                                    emit();
                                }
                                invocation.return_value(Some(&(handle,).to_variant()));
                                if matches!(behavior, Behavior::Respond(_, false, _)) {
                                    emit();
                                }
                                if matches!(behavior, Behavior::LoseOwner) {
                                    connection
                                        .call_sync(
                                            Some("org.freedesktop.DBus"),
                                            "/org/freedesktop/DBus",
                                            "org.freedesktop.DBus",
                                            "ReleaseName",
                                            Some(&(PORTAL_BUS_NAME,).to_variant()),
                                            None,
                                            gio::DBusCallFlags::NONE,
                                            1000,
                                            gio::Cancellable::NONE,
                                        )
                                        .unwrap();
                                }
                            },
                            |_, _, _, _, _| false.to_variant(),
                            |_, _, _, _, _, _| false,
                        )
                        .unwrap();
                    bus.call_sync(
                        Some("org.freedesktop.DBus"),
                        "/org/freedesktop/DBus",
                        "org.freedesktop.DBus",
                        "RequestName",
                        Some(&(PORTAL_BUS_NAME, 0u32).to_variant()),
                        None,
                        gio::DBusCallFlags::NONE,
                        1000,
                        gio::Cancellable::NONE,
                    )
                    .unwrap();
                    ready.send(()).unwrap();
                    while !stop.load(Ordering::Acquire) {
                        for _ in 0..32 {
                            if !context.pending() {
                                break;
                            }
                            context.iteration(false);
                        }
                        thread::sleep(Duration::from_millis(1));
                    }
                    bus.unregister_object(registration).unwrap();
                    let _ = bus.close_sync(gio::Cancellable::NONE);
                })
                .unwrap();
        });
        receiver.recv_timeout(Duration::from_secs(3)).unwrap();
        Self {
            _daemon: daemon,
            address,
            stopped,
            worker: Some(worker),
            closed,
            methods,
        }
    }

    fn bus(&self) -> gio::DBusConnection {
        let context = glib::MainContext::new();
        context
            .with_thread_default(|| connect(&context, &self.address))
            .unwrap()
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        self.stopped.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            worker.join().unwrap();
        }
    }
}

#[test]
fn all_open_operations_observe_final_success_cancellation_and_failure() {
    let dir = std::env::temp_dir().join(format!("portal-request-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir(&dir).unwrap();
    let file = dir.join("a é file.txt");
    std::fs::write(&file, "fixture").unwrap();
    for code in [0, 1, 2] {
        for early in [false, true] {
            let fixture = Fixture::new(Behavior::Respond(code, early, true));
            let bus = fixture.bus();
            for result in [
                portal_open_uri(&bus, "", "https://example.invalid/"),
                portal_open_fd(&bus, "OpenFile", "", &file),
                portal_open_fd(&bus, "OpenDirectory", "", &dir),
            ] {
                match code {
                    0 => assert_eq!(result, Ok(())),
                    1 => assert_eq!(result, Err(ServiceError::Cancelled)),
                    _ => assert!(matches!(result, Err(ServiceError::Failed(_)))),
                }
            }
            assert_eq!(
                *fixture.methods.lock().unwrap(),
                ["OpenURI", "OpenFile", "OpenDirectory"]
            );
            assert_eq!(fixture.closed.load(Ordering::SeqCst), 0);
        }
    }
    std::fs::remove_dir_all(dir).unwrap();
}

fn invoke(bus: &gio::DBusConnection, timeout: Duration) -> Result<(), ServiceError> {
    run_with_timeout(bus, timeout, |owner, options| {
        bus.call_sync(
            Some(owner),
            PORTAL_OBJECT_PATH,
            OPEN_URI_INTERFACE,
            "OpenURI",
            Some(&glib::Variant::tuple_from_iter([
                "".to_variant(),
                "https://example.invalid/".to_variant(),
                options,
            ])),
            None,
            gio::DBusCallFlags::NONE,
            CALL_TIMEOUT_MS,
            gio::Cancellable::NONE,
        )
        .map_err(|error| ServiceError::from_call(&error))
    })
}

#[test]
fn unfinished_requests_are_closed_and_subscriptions_do_not_reach_the_next_request() {
    let fixture = Fixture::new(Behavior::Never);
    let bus = fixture.bus();
    for count in 1..=2 {
        assert!(
            matches!(invoke(&bus, Duration::from_millis(30)), Err(ServiceError::Failed(reason)) if reason.contains("timed out"))
        );
        assert_eq!(fixture.closed.load(Ordering::SeqCst), count);
    }
}

#[test]
fn owner_disappearance_is_final_and_initial_unsupported_transport_can_fall_back() {
    let fixture = Fixture::new(Behavior::LoseOwner);
    assert!(
        matches!(invoke(&fixture.bus(), Duration::from_secs(2)), Err(ServiceError::Failed(reason)) if reason.contains("disconnected"))
    );
    let fixture = Fixture::new(Behavior::Unsupported);
    assert_eq!(
        invoke(&fixture.bus(), Duration::from_secs(2)),
        Err(ServiceError::Unavailable)
    );
}

#[test]
fn invalid_response_and_request_shapes_are_failures() {
    assert!(matches!(
        response(&().to_variant()),
        Err(ServiceError::Failed(_))
    ));
    assert!(matches!(
        response(&(9u32, HashMap::<String, glib::Variant>::new()).to_variant()),
        Err(ServiceError::Failed(_))
    ));
    assert!(matches!(
        request_path(&("wrong type",).to_variant()),
        Err(ServiceError::Failed(_))
    ));
}
