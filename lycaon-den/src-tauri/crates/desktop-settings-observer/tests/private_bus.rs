#![cfg(target_os = "linux")]
use desktop_settings_observer::{read, Status};
use glib::ToVariant;
use std::{
    io::{BufRead, BufReader},
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc,
    },
    thread,
    time::{Duration, Instant},
};
struct Bus {
    child: Child,
    address: String,
}
impl Bus {
    fn new() -> Self {
        let mut child = Command::new("dbus-daemon")
            .args(["--session", "--nofork", "--print-address=1"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let mut address = String::new();
        BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut address)
            .unwrap();
        Self {
            child,
            address: address.trim().into(),
        }
    }
    fn observer(&self) -> Child {
        Command::new(env!("CARGO_BIN_EXE_pw-desktop-settings"))
            .env_clear()
            .env("DBUS_SESSION_BUS_ADDRESS", &self.address)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap()
    }
}
impl Drop for Bus {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}
fn ended(child: &mut Child) {
    let deadline = Instant::now() + Duration::from_secs(4);
    while Instant::now() < deadline {
        if let Some(status) = child.try_wait().unwrap() {
            assert!(status.success());
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    let _ = child.kill();
    let _ = child.wait();
    panic!("owned observer failed to stop");
}
#[test]
fn missing_service_reports_typed_unavailability_and_exits() {
    let bus = Bus::new();
    let mut observer = bus.observer();
    let frame = read(&mut BufReader::new(observer.stdout.take().unwrap()), 0)
        .unwrap()
        .unwrap();
    assert_eq!(frame.status, Status::Missing);
    assert!(frame.settings.is_empty());
    ended(&mut observer);
}
struct Service {
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}
impl Service {
    fn new(bus: &Bus, denied: bool) -> Self {
        let address = bus.address.clone();
        let stop = Arc::new(AtomicBool::new(false));
        let requested = stop.clone();
        let (ready, wait) = mpsc::channel();
        let worker = thread::spawn(move || {
            let context = glib::MainContext::new();
            context.with_thread_default(|| {
                let connection=gio::DBusConnection::for_address_sync(&address,
                    gio::DBusConnectionFlags::AUTHENTICATION_CLIENT|gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
                    None::<&gio::DBusAuthObserver>,None::<&gio::Cancellable>).unwrap();
                let xml="<node><interface name='org.freedesktop.portal.Settings'><method name='ReadAll'><arg type='as' direction='in'/><arg type='a{sa{sv}}' direction='out'/></method><signal name='SettingChanged'><arg type='s'/><arg type='s'/><arg type='v'/></signal></interface></node>";
                let info=gio::DBusNodeInfo::for_xml(xml).unwrap().lookup_interface("org.freedesktop.portal.Settings").unwrap();
                let _registration=connection.register_object("/org/freedesktop/portal/desktop",&info,
                    move |_,_,_,_,method,args,invocation| {
                        assert_eq!(method,"ReadAll");
                        assert_eq!(args.get::<(Vec<String>,)>().unwrap().0,desktop_settings_observer::NAMESPACES);
                        if denied { invocation.return_dbus_error("org.freedesktop.DBus.Error.AccessDenied","fixture refusal"); }
                        else { let values=std::collections::BTreeMap::from([("org.freedesktop.appearance",std::collections::BTreeMap::from([("contrast",1u32.to_variant())]))]);invocation.return_value(Some(&(values,).to_variant())); }
                    },|_,_,_,_,_|false.to_variant(),|_,_,_,_,_,_|false).unwrap();
                connection.call_sync(Some("org.freedesktop.DBus"),"/org/freedesktop/DBus","org.freedesktop.DBus","RequestName",
                    Some(&("org.freedesktop.portal.Desktop",0u32).to_variant()),None,gio::DBusCallFlags::NONE,1500,None::<&gio::Cancellable>).unwrap();
                ready.send(()).unwrap();
                while !requested.load(Ordering::Acquire) { context.iteration(false);thread::sleep(Duration::from_millis(5)); }
                connection.close_sync(None::<&gio::Cancellable>).unwrap();
            }).unwrap();
        });
        wait.recv_timeout(Duration::from_secs(5)).unwrap();
        Self {
            stop,
            worker: Some(worker),
        }
    }
}
impl Drop for Service {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        self.worker.take().unwrap().join().unwrap();
    }
}
#[test]
fn denied_service_is_reported_without_alternate_identity_or_default_snapshot() {
    let bus = Bus::new();
    let _service = Service::new(&bus, true);
    let mut observer = bus.observer();
    let frame = read(&mut BufReader::new(observer.stdout.take().unwrap()), 0)
        .unwrap()
        .unwrap();
    assert_eq!(frame.status, Status::Denied);
    assert!(frame.settings.is_empty());
    ended(&mut observer);
}
#[test]
fn ready_observer_exits_on_parent_pipe_eof() {
    let bus = Bus::new();
    let _service = Service::new(&bus, false);
    let mut observer = bus.observer();
    let frame = read(&mut BufReader::new(observer.stdout.take().unwrap()), 0)
        .unwrap()
        .unwrap();
    assert_eq!(frame.status, Status::Ready);
    assert_eq!(
        frame.settings,
        vec![desktop_settings_observer::Setting::Contrast(1)]
    );
    drop(observer.stdin.take());
    ended(&mut observer);
}
