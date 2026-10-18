use super::*;
use std::io::{BufRead, BufReader, Read};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::process::{Child, Command, Stdio};
use std::sync::{
    atomic::{AtomicBool, AtomicUsize, Ordering},
    mpsc, Arc,
};
use std::thread;

const INTERFACE: &str = r#"<node><interface name="org.freedesktop.portal.Documents">
<method name="AddFull"><arg type="ah" direction="in"/><arg type="u" direction="in"/><arg type="s" direction="in"/><arg type="as" direction="in"/><arg type="as" direction="out"/><arg type="a{sv}" direction="out"/></method>
<method name="Delete"><arg type="s" direction="in"/></method>
</interface></node>"#;

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
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
    deletes: Arc<AtomicUsize>,
}

fn connect(context: &glib::MainContext, address: &str) -> gio::DBusConnection {
    crate::linux_desktop::bus::connect(context, address, Duration::from_secs(2)).unwrap()
}

impl Fixture {
    fn new(expected: &Path, valid_mount: bool, refuse: bool) -> Self {
        let mut daemon = Daemon(
            Command::new("dbus-daemon")
                .args(["--session", "--nofork", "--print-address=1"])
                .stdout(Stdio::piped())
                .stderr(Stdio::inherit())
                .spawn()
                .expect("start required document portal fixture"),
        );
        let output = daemon.0.stdout.take().unwrap();
        let (send, receive) = mpsc::sync_channel(1);
        thread::spawn(move || {
            let mut address = String::new();
            BufReader::new(output)
                .take(4096)
                .read_line(&mut address)
                .unwrap();
            let _ = send.send(address.trim_end().to_string());
        });
        let address = receive.recv_timeout(Duration::from_secs(2)).unwrap();
        let server_address = address.clone();
        let expected = expected.to_path_buf();
        let expected_flags = if expected.is_dir() { 8 } else { 0 };
        let stop = Arc::new(AtomicBool::new(false));
        let stopped = stop.clone();
        let deletes = Arc::new(AtomicUsize::new(0));
        let deleted = deletes.clone();
        let (send, receive) = mpsc::sync_channel(1);
        let worker = thread::spawn(move || {
            let context = glib::MainContext::new();
            context
                .with_thread_default(|| {
                    let bus = connect(&context, &server_address);
                    let node = gio::DBusNodeInfo::for_xml(INTERFACE).unwrap();
                    let registration = bus
                        .register_object(
                            OBJECT,
                            &node.lookup_interface(SERVICE).unwrap(),
                            move |_, _, _, _, method, parameters, invocation| match method {
                                "AddFull" => {
                                    let (handles, flags, app, permissions) = parameters
                                        .get::<(Vec<Handle>, u32, String, Vec<String>)>()
                                        .unwrap();
                                    assert_eq!(flags, expected_flags);
                                    assert_eq!(app, "com.example.Editor");
                                    assert_eq!(permissions, ["read", "write"]);
                                    assert_eq!(handles.len(), 1);
                                    let descriptors = invocation.message().unix_fd_list().unwrap();
                                    // GIO duplicates this descriptor; the fixture owns that duplicate.
                                    let fd = unsafe {
                                        OwnedFd::from_raw_fd(descriptors.get(handles[0].0).unwrap())
                                    };
                                    assert_eq!(
                                        std::fs::read_link(format!(
                                            "/proc/self/fd/{}",
                                            fd.as_raw_fd()
                                        ))
                                        .unwrap(),
                                        expected
                                    );
                                    if refuse {
                                        invocation.return_dbus_error(
                                            "org.freedesktop.portal.Error.NotAllowed",
                                            "fixture refusal",
                                        );
                                    } else {
                                        let mount = if valid_mount {
                                            b"/run/user/1000/doc\0".to_vec()
                                        } else {
                                            b"relative\0".to_vec()
                                        };
                                        invocation.return_value(Some(
                                            &(
                                                vec!["unique123"],
                                                HashMap::from([("mountpoint", mount.to_variant())]),
                                            )
                                                .to_variant(),
                                        ));
                                    }
                                }
                                "Delete" => {
                                    assert_eq!(
                                        parameters.get::<(String,)>().unwrap().0,
                                        "unique123"
                                    );
                                    deleted.fetch_add(1, Ordering::SeqCst);
                                    invocation.return_value(Some(&().to_variant()));
                                }
                                _ => panic!("unexpected documents method {method}"),
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
                        Some(&(SERVICE, 0u32).to_variant()),
                        None,
                        gio::DBusCallFlags::NONE,
                        1000,
                        gio::Cancellable::NONE,
                    )
                    .unwrap();
                    send.send(()).unwrap();
                    while !stopped.load(Ordering::Acquire) {
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
        receive.recv_timeout(Duration::from_secs(3)).unwrap();
        Self {
            _daemon: daemon,
            address,
            stop,
            worker: Some(worker),
            deletes,
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
        self.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            worker.join().unwrap();
        }
    }
}

#[test]
fn exact_document_grants_and_unused_export_cleanup_use_the_portal_protocol() {
    let temp = crate::test_support::TempDir::new("flatpak-document-export");
    let file = temp.path().join("a é file.txt");
    std::fs::write(&file, "fixture").unwrap();
    for path in [file.as_path(), temp.path()] {
        let fixture = Fixture::new(path, true, false);
        let document = export_on(&fixture.bus(), path, "com.example.Editor").unwrap();
        assert_eq!(document.path.file_name(), path.file_name());
        assert!(document.path.starts_with("/run/user/1000/doc/unique123"));
        drop(document);
        assert_eq!(fixture.deletes.load(Ordering::SeqCst), 1);
        export_on(&fixture.bus(), path, "com.example.Editor")
            .unwrap()
            .retain();
        assert_eq!(fixture.deletes.load(Ordering::SeqCst), 1);
    }
    let invalid = Fixture::new(&file, false, false);
    assert!(export_on(&invalid.bus(), &file, "com.example.Editor").is_err());
    assert_eq!(invalid.deletes.load(Ordering::SeqCst), 1);
    let refused = Fixture::new(&file, true, true);
    assert!(export_on(&refused.bus(), &file, "com.example.Editor").is_err());
    assert_eq!(refused.deletes.load(Ordering::SeqCst), 0);
}
