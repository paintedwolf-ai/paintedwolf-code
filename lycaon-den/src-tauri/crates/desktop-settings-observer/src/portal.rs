//! The executable reads only fixed desktop preference keys; it accepts no requests.
mod portal_decode;

use desktop_settings_observer::{Frame, Setting, Status, NAMESPACES};
use glib::{prelude::ToVariant, VariantTy};
use portal_decode::{status, value, Journal};
use std::{
    cell::{Cell, RefCell},
    io::{self, Read},
    rc::Rc,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::Duration,
};
const BUS: &str = "org.freedesktop.portal.Desktop";
const PATH: &str = "/org/freedesktop/portal/desktop";
const INTERFACE: &str = "org.freedesktop.portal.Settings";
const TIMEOUT: Duration = Duration::from_millis(1500);

struct Output {
    sequence: u64,
}
impl Output {
    fn send(&mut self, status: Status, settings: Vec<Setting>) -> io::Result<()> {
        self.sequence = self
            .sequence
            .checked_add(1)
            .ok_or_else(|| io::Error::other("settings sequence exhausted"))?;
        Frame {
            schema_version: 1,
            sequence: self.sequence,
            status,
            settings,
        }
        .write(&mut io::stdout().lock())
    }
}
pub fn run() -> io::Result<()> {
    // This distinct ELF carries no secrets and is never a custody-trusted image.
    if unsafe { libc::prctl(libc::PR_GET_DUMPABLE) } != 1 {
        return Err(io::Error::other(
            "settings observer must be independently dumpable",
        ));
    }
    let stop = Arc::new(AtomicBool::new(false));
    let input_stop = stop.clone();
    std::thread::spawn(move || {
        let _ = io::stdin().read(&mut [0u8; 1]);
        input_stop.store(true, Ordering::Release);
    });
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| watch(&context, stop))
        .map_err(|_| io::Error::other("settings main context unavailable"))?
}
fn watch(context: &glib::MainContext, stop: Arc<AtomicBool>) -> io::Result<()> {
    let output = Rc::new(RefCell::new(Output { sequence: 0 }));
    let connection = match context.block_on(glib::future_with_timeout(
        TIMEOUT,
        gio::bus_get_future(gio::BusType::Session),
    )) {
        Ok(Ok(connection)) => connection,
        Ok(Err(error)) => return output.borrow_mut().send(status(&error), vec![]),
        Err(_) => return output.borrow_mut().send(Status::TimedOut, vec![]),
    };
    let loop_ = glib::MainLoop::new(Some(context), false);
    let journal = Rc::new(RefCell::new(Journal::default()));
    let lost = Rc::new(Cell::new(false));
    let changes = journal.clone();
    let malformed = lost.clone();
    let malformed_loop = loop_.clone();
    let malformed_output = output.clone();
    connection.signal_subscribe(
        Some(BUS),
        Some(INTERFACE),
        Some("SettingChanged"),
        Some(PATH),
        None,
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if parameters.size() > 64 * 1024 {
                malformed.set(true);
                let _ = malformed_output.borrow_mut().send(Status::Failed, vec![]);
                malformed_loop.quit();
                return;
            }
            if parameters.type_().as_str() != "(ssv)" {
                return;
            }
            let n = parameters.child_value(0);
            let k = parameters.child_value(1);
            if let (Some(n), Some(k)) = (n.str(), k.str()) {
                changes
                    .borrow_mut()
                    .change(n, k, value(n, k, &parameters.child_value(2)));
            }
        },
    );
    let owner_lost = lost.clone();
    let owner_loop = loop_.clone();
    let owner_output = output.clone();
    connection.signal_subscribe(
        Some("org.freedesktop.DBus"),
        Some("org.freedesktop.DBus"),
        Some("NameOwnerChanged"),
        Some("/org/freedesktop/DBus"),
        Some(BUS),
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if let Some((_, old, new)) = parameters.get::<(String, String, String)>() {
                if !old.is_empty() && old != new {
                    owner_lost.set(true);
                    let _ = owner_output.borrow_mut().send(Status::Disconnected, vec![]);
                    owner_loop.quit();
                }
            }
        },
    );
    let reply = context.block_on(glib::future_with_timeout(
        TIMEOUT,
        connection.call_future(
            Some(BUS),
            PATH,
            INTERFACE,
            "ReadAll",
            Some(&(NAMESPACES.to_vec(),).to_variant()),
            Some(VariantTy::new("(a{sa{sv}})").unwrap()),
            gio::DBusCallFlags::NONE,
            1500,
        ),
    ));
    if lost.get() {
        return Ok(());
    }
    match reply {
        Ok(Ok(reply)) => {
            if !journal.borrow_mut().snapshot(&reply) {
                return output.borrow_mut().send(Status::Failed, vec![]);
            }
        }
        Ok(Err(error)) => return output.borrow_mut().send(status(&error), vec![]),
        Err(_) => return output.borrow_mut().send(Status::TimedOut, vec![]),
    }
    output
        .borrow_mut()
        .send(Status::Ready, journal.borrow().settings.clone())?;
    journal.borrow_mut().dirty = false;
    let ticks = Rc::new(Cell::new(0u16));
    let tick_loop = loop_.clone();
    let tick_connection = connection.clone();
    let timer = context.spawn_local(async move {
        loop {
            glib::timeout_future(Duration::from_millis(100)).await;
            if stop.load(Ordering::Acquire) {
                tick_loop.quit();
                break;
            }
            if tick_connection.is_closed() {
                let _ = output.borrow_mut().send(Status::Disconnected, vec![]);
                tick_loop.quit();
                break;
            }
            ticks.set(ticks.get() + 1);
            let mut journal = journal.borrow_mut();
            if journal.dirty || ticks.get() >= 100 {
                if output
                    .borrow_mut()
                    .send(Status::Ready, journal.settings.clone())
                    .is_err()
                {
                    tick_loop.quit();
                    break;
                }
                journal.dirty = false;
                ticks.set(0);
            }
        }
    });
    loop_.run();
    timer.abort();
    Ok(())
}
#[cfg(test)]
#[path = "portal_tests.rs"]
mod tests;
