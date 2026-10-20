//! The executable reads only fixed desktop preference keys; it accepts no requests.
use desktop_settings_observer::{Frame, Setting, Status, NAMESPACES};
use gio::prelude::*;
use glib::{ToVariant, Variant, VariantTy};
use std::{cell::{Cell, RefCell}, io::{self, Read}, rc::Rc, sync::{Arc, atomic::{AtomicBool, Ordering}}, time::Duration};
const BUS: &str = "org.freedesktop.portal.Desktop";
const PATH: &str = "/org/freedesktop/portal/desktop";
const INTERFACE: &str = "org.freedesktop.portal.Settings";
const TIMEOUT: Duration = Duration::from_millis(1500);

fn status(error: &glib::Error) -> Status {
    if error.matches(gio::DBusError::AccessDenied) || error.matches(gio::DBusError::AuthFailed) || error.matches(gio::IOErrorEnum::PermissionDenied) { Status::Denied }
    else if error.matches(gio::DBusError::ServiceUnknown) || error.matches(gio::DBusError::NameHasNoOwner)
        || error.matches(gio::DBusError::UnknownInterface) || error.matches(gio::DBusError::UnknownMethod) { Status::Missing }
    else if error.matches(gio::DBusError::Disconnected) { Status::Disconnected }
    else if error.matches(gio::DBusError::NoReply) || error.matches(gio::IOErrorEnum::TimedOut) { Status::TimedOut }
    else { Status::Failed }
}
fn value(namespace: &str, key: &str, boxed: &Variant) -> Option<Setting> {
    let v = boxed.as_variant().unwrap_or_else(|| boxed.clone());
    let setting = match (namespace, key) {
        ("org.freedesktop.appearance", "contrast") => Setting::Contrast(v.get()?),
        ("org.freedesktop.appearance", "accent-color") => { let (r,g,b) = v.get::<(f64,f64,f64)>()?; Setting::Accent([r,g,b]) },
        ("org.gnome.desktop.interface", "text-scaling-factor") => Setting::TextScale(v.get()?),
        ("org.gnome.desktop.a11y.interface", "high-contrast") => Setting::HighContrast(v.get()?),
        ("org.kde.kdeglobals.General", "AccentColor") => Setting::KdeAccent(v.str()?.into()),
        ("org.gnome.desktop.wm.preferences", "button-layout") => Setting::ButtonLayout(v.str()?.into()),
        _ => return None,
    };
    setting.valid().then_some(setting)
}
struct Output { sequence: u64 }
impl Output {
    fn send(&mut self, status: Status, settings: Vec<Setting>) -> io::Result<()> {
        self.sequence = self.sequence.checked_add(1).ok_or_else(|| io::Error::other("settings sequence exhausted"))?;
        Frame { schema_version:1, sequence:self.sequence, status, settings }.write(&mut io::stdout().lock())
    }
}
#[derive(Default)]
struct Journal { settings: Vec<Setting>, pending: Vec<(String,String,Option<Setting>)>, ready: bool, dirty: bool }
impl Journal {
    fn change(&mut self, namespace: &str, key: &str, setting: Option<Setting>) {
        if !known(namespace,key) { return; }
        if !self.ready {
            self.pending.retain(|(n,k,_)| n != namespace || k != key);
            self.pending.push((namespace.into(),key.into(),setting)); return;
        }
        self.settings.retain(|s| s.identity() != (namespace,key));
        if let Some(setting) = setting { self.settings.push(setting); }
        self.dirty = true;
    }
    fn snapshot(&mut self, reply: &Variant) {
        self.settings.clear();
        for entry in reply.child_value(0).iter() {
            if let Some(namespace) = entry.child_value(0).str() {
                for row in entry.child_value(1).iter() {
                    if let Some(key) = row.child_value(0).str() {
                        if let Some(setting) = value(namespace,key,&row.child_value(1)) { self.settings.push(setting); }
                    }
                }
            }
        }
        self.ready = true;
        for (namespace,key,setting) in std::mem::take(&mut self.pending) { self.change(&namespace,&key,setting); }
        self.dirty = true;
    }
}
fn known(namespace: &str, key: &str) -> bool {
    matches!((namespace,key), ("org.freedesktop.appearance","contrast"|"accent-color")
        |("org.gnome.desktop.interface","text-scaling-factor")|("org.gnome.desktop.a11y.interface","high-contrast")
        |("org.kde.kdeglobals.General","AccentColor")|("org.gnome.desktop.wm.preferences","button-layout"))
}
pub fn run() -> io::Result<()> {
    // This distinct ELF carries no secrets and is never a custody-trusted image.
    if unsafe { libc::prctl(libc::PR_GET_DUMPABLE) } != 1 { return Err(io::Error::other("settings observer must be independently dumpable")); }
    let stop = Arc::new(AtomicBool::new(false));
    let input_stop = stop.clone();
    std::thread::spawn(move || { let _ = io::stdin().read(&mut [0u8;1]); input_stop.store(true,Ordering::Release); });
    let context = glib::MainContext::new();
    context.with_thread_default(|| watch(&context,stop)).map_err(|_| io::Error::other("settings main context unavailable"))?
}
fn watch(context: &glib::MainContext, stop: Arc<AtomicBool>) -> io::Result<()> {
    let output = Rc::new(RefCell::new(Output { sequence:0 }));
    let connection = match context.block_on(glib::future_with_timeout(TIMEOUT,gio::bus_get_future(gio::BusType::Session))) {
        Ok(Ok(connection)) => connection,
        Ok(Err(error)) => return output.borrow_mut().send(status(&error),vec![]),
        Err(_) => return output.borrow_mut().send(Status::TimedOut,vec![]),
    };
    let loop_ = glib::MainLoop::new(Some(context),false);
    let journal = Rc::new(RefCell::new(Journal::default()));
    let lost = Rc::new(Cell::new(false));
    let changes = journal.clone();
    connection.signal_subscribe(Some(BUS),Some(INTERFACE),Some("SettingChanged"),Some(PATH),None,gio::DBusSignalFlags::NONE,
        move |_,_,_,_,_,parameters| {
            if parameters.type_().as_str() != "(ssv)" { return; }
            let n=parameters.child_value(0);let k=parameters.child_value(1);
            if let (Some(n),Some(k))=(n.str(),k.str()) { changes.borrow_mut().change(n,k,value(n,k,&parameters.child_value(2))); }
        });
    let owner_lost=lost.clone();let owner_loop=loop_.clone();let owner_output=output.clone();
    connection.signal_subscribe(Some("org.freedesktop.DBus"),Some("org.freedesktop.DBus"),Some("NameOwnerChanged"),Some("/org/freedesktop/DBus"),Some(BUS),gio::DBusSignalFlags::NONE,
        move |_,_,_,_,_,parameters| {
            if let Some((_,old,new))=parameters.get::<(String,String,String)>() {
                if !old.is_empty() && old!=new { owner_lost.set(true);let _=owner_output.borrow_mut().send(Status::Disconnected,vec![]);owner_loop.quit(); }
            }
        });
    let reply=context.block_on(glib::future_with_timeout(TIMEOUT,connection.call_future(Some(BUS),PATH,INTERFACE,"ReadAll",
        Some(&(NAMESPACES.to_vec(),).to_variant()),Some(VariantTy::new("(a{sa{sv}})").unwrap()),gio::DBusCallFlags::NONE,1500)));
    if lost.get() { return Ok(()); }
    match reply {
        Ok(Ok(reply)) => journal.borrow_mut().snapshot(&reply),
        Ok(Err(error)) => return output.borrow_mut().send(status(&error),vec![]),
        Err(_) => return output.borrow_mut().send(Status::TimedOut,vec![]),
    }
    output.borrow_mut().send(Status::Ready,journal.borrow().settings.clone())?;
    journal.borrow_mut().dirty=false;
    let ticks=Rc::new(Cell::new(0u16));
    let tick_loop=loop_.clone();
    let tick_connection=connection.clone();
    let timer=context.spawn_local(async move {
        loop {
            glib::timeout_future(Duration::from_millis(100)).await;
            if stop.load(Ordering::Acquire) { tick_loop.quit(); break; }
            if tick_connection.is_closed() { let _=output.borrow_mut().send(Status::Disconnected,vec![]);tick_loop.quit();break; }
            ticks.set(ticks.get()+1);
            let mut journal=journal.borrow_mut();
            if journal.dirty || ticks.get()>=100 {
                if output.borrow_mut().send(Status::Ready,journal.settings.clone()).is_err() { tick_loop.quit();break; }
                journal.dirty=false;ticks.set(0);
            }
        }
    });
    loop_.run();timer.abort();Ok(())
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test] fn typed_denial_cannot_become_missing() {
        assert_eq!(status(&glib::Error::new(gio::DBusError::AccessDenied,"diagnostic")),Status::Denied);
        assert_eq!(status(&glib::Error::new(gio::DBusError::ServiceUnknown,"same diagnostic")),Status::Missing);
        assert_eq!(status(&glib::Error::new(gio::DBusError::Failed,"same diagnostic")),Status::Failed);
    }
    #[test] fn settings_keys_and_variant_types_are_fixed() {
        assert_eq!(value(NAMESPACES[0],"contrast",&1u32.to_variant()),Some(Setting::Contrast(1)));
        assert!(value(NAMESPACES[0],"contrast",&"one".to_variant()).is_none());
        assert!(value(NAMESPACES[0],"execute-command",&"anything".to_variant()).is_none());
        assert!(value(NAMESPACES[0],"accent-color",&(2.0,0.0,0.0).to_variant()).is_none());
    }
    #[test] fn startup_snapshot_replays_intervening_changes_and_invalidates_removed_fact() {
        let mut journal=Journal::default();
        journal.change(NAMESPACES[0],"contrast",Some(Setting::Contrast(1)));
        let namespaces=std::collections::BTreeMap::from([(NAMESPACES[0],std::collections::BTreeMap::from([("contrast",0u32.to_variant())]))]);
        journal.snapshot(&(namespaces,).to_variant());
        assert_eq!(journal.settings,vec![Setting::Contrast(1)]);
        journal.change(NAMESPACES[0],"contrast",None);
        assert!(journal.settings.is_empty());
        assert!(journal.dirty);
    }
}
