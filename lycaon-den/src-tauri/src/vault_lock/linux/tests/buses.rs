//! Private dbus-daemons and fixture services standing in for logind and the
//! desktop's screen saver.

use super::*;

pub(super) const SESSION_PATH: &str = "/org/freedesktop/login1/session/fixture";
pub(super) const LOGIN_PATH: &str = "/org/freedesktop/login1";
pub(super) const MANAGER: &str = "org.freedesktop.login1.Manager";
pub(super) const SCREENSAVER: &str = "org.freedesktop.ScreenSaver";
pub(super) const SCREENSAVER_PATH: &str = "/org/freedesktop/ScreenSaver";

pub(super) fn uid() -> u32 {
    // SAFETY: getuid has no preconditions and cannot fail.
    unsafe { libc::getuid() }
}

/// A private dbus-daemon listening on a fixed socket, so it can be stopped
/// and started again at the same address.
pub(super) struct Daemon {
    child: Option<Child>,
    pub(super) root: PathBuf,
    socket: PathBuf,
}

impl Daemon {
    pub(super) fn new(name: &str) -> Self {
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

    pub(super) fn address(&self) -> String {
        format!("unix:path={}", self.socket.display())
    }

    pub(super) fn start(&mut self) {
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

    pub(super) fn stop(&mut self) {
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
pub(super) struct Facts {
    pub(super) active: bool,
    pub(super) locked: bool,
    pub(super) sleeping: bool,
    pub(super) screensaver: bool,
    pub(super) session_uid: u32,
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

pub(super) enum Request {
    Emit {
        path: &'static str,
        interface: &'static str,
        member: &'static str,
        parameters: glib::Variant,
    },
    Release(&'static str),
}

/// One fixture connection serving its objects on its own thread.
pub(super) struct Service {
    requests: mpsc::Sender<Request>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

pub(super) const LOGIND_XML: &str = "<node>\
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

pub(super) const SCREENSAVER_XML: &str = "<node><interface name='org.freedesktop.ScreenSaver'>\
<method name='GetActive'><arg type='b' direction='out'/></method>\
<signal name='ActiveChanged'><arg type='b'/></signal></interface></node>";

impl Service {
    pub(super) fn logind(daemon: &Daemon, facts: Arc<Mutex<Facts>>) -> Self {
        Self::start(daemon.address(), facts, true)
    }

    pub(super) fn screensaver(daemon: &Daemon, facts: Arc<Mutex<Facts>>) -> Self {
        Self::start(daemon.address(), facts, false)
    }

    pub(super) fn start(address: String, facts: Arc<Mutex<Facts>>, logind: bool) -> Self {
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

    pub(super) fn emit(
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

    pub(super) fn release(&self, name: &'static str) {
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

pub(super) fn request_name(connection: &gio::DBusConnection, name: &str) {
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

pub(super) fn register_logind(
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

pub(super) fn register_screensaver(
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
