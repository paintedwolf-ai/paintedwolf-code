//! Graphical-session identity from logind's process and user records.

use gtk::glib::ToVariant;
use gtk::{gio, glib};
use serde::Serialize;
use std::time::Duration;

use super::bus;

pub(crate) const NAME: &str = "org.freedesktop.login1";
pub(crate) const SESSION_INTERFACE: &str = "org.freedesktop.login1.Session";
const ROOT: &str = "/org/freedesktop/login1";
const CALL_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub(crate) struct GraphicalSession {
    pub id: String,
    pub path: String,
    pub uid: u32,
    pub session_type: String,
    pub active: bool,
    pub can_lock: bool,
    pub locked: bool,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum SessionUnavailable {
    NoGraphicalSession,
    Unreadable,
    InvalidProperties,
    WrongUser,
}

/// A user-manager or SSH-launched app can have no graphical pid session.
/// Its logind User.Display record supplies the alternative, with the same checks.
pub(crate) fn discover(
    context: &glib::MainContext,
    system: &gio::DBusConnection,
) -> Result<GraphicalSession, SessionUnavailable> {
    // SAFETY: getuid has no preconditions and cannot fail.
    let uid = unsafe { libc::getuid() };
    let by_pid = bus::call(
        context,
        system,
        Some(NAME),
        ROOT,
        "org.freedesktop.login1.Manager",
        "GetSessionByPID",
        Some(&(std::process::id(),).to_variant()),
        CALL_TIMEOUT,
    );
    if let Ok(reply) = by_pid {
        if let Some(path) = object_path(&reply) {
            match read(context, system, &path, uid) {
                Ok(session) => return Ok(session),
                Err(SessionUnavailable::WrongUser) => return Err(SessionUnavailable::WrongUser),
                Err(_) => {}
            }
        }
    }
    let user = bus::call(
        context,
        system,
        Some(NAME),
        ROOT,
        "org.freedesktop.login1.Manager",
        "GetUser",
        Some(&(uid,).to_variant()),
        CALL_TIMEOUT,
    )
    .map_err(|_| SessionUnavailable::Unreadable)?;
    let user_path = object_path(&user).ok_or(SessionUnavailable::InvalidProperties)?;
    let display = bus::call(
        context,
        system,
        Some(NAME),
        &user_path,
        "org.freedesktop.DBus.Properties",
        "Get",
        Some(&("org.freedesktop.login1.User", "Display").to_variant()),
        CALL_TIMEOUT,
    )
    .map_err(|_| SessionUnavailable::Unreadable)?;
    let path = display_path(&display).ok_or(SessionUnavailable::NoGraphicalSession)?;
    read(context, system, &path, uid)
}

pub(crate) fn read(
    context: &glib::MainContext,
    system: &gio::DBusConnection,
    path: &str,
    uid: u32,
) -> Result<GraphicalSession, SessionUnavailable> {
    let reply = bus::call(
        context,
        system,
        Some(NAME),
        path,
        "org.freedesktop.DBus.Properties",
        "GetAll",
        Some(&(SESSION_INTERFACE,).to_variant()),
        CALL_TIMEOUT,
    )
    .map_err(|_| SessionUnavailable::Unreadable)?;
    parse_properties(path, uid, &reply)
}

fn object_path(reply: &glib::Variant) -> Option<String> {
    if !reply.is_type(glib::VariantTy::new("(o)").ok()?) {
        return None;
    }
    let path = reply.child_value(0).str()?.to_owned();
    (path != "/").then_some(path)
}

fn display_path(reply: &glib::Variant) -> Option<String> {
    if !reply.is_type(glib::VariantTy::new("(v)").ok()?) {
        return None;
    }
    let value = reply.child_value(0).as_variant()?;
    if !value.is_type(glib::VariantTy::new("(so)").ok()?) {
        return None;
    }
    let path = value.child_value(1).str()?.to_owned();
    (path != "/").then_some(path)
}

fn parse_properties(
    path: &str,
    uid: u32,
    reply: &glib::Variant,
) -> Result<GraphicalSession, SessionUnavailable> {
    let invalid = || SessionUnavailable::InvalidProperties;
    if !reply.is_type(glib::VariantTy::new("(a{sv})").map_err(|_| invalid())?) {
        return Err(invalid());
    }
    let properties = glib::VariantDict::new(Some(&reply.child_value(0)));
    let kind = properties
        .lookup::<String>("Type")
        .map_err(|_| invalid())?
        .ok_or_else(invalid)?;
    let class = properties
        .lookup::<String>("Class")
        .map_err(|_| invalid())?
        .ok_or_else(invalid)?;
    if !matches!(kind.as_str(), "x11" | "wayland")
        || !matches!(
            class.as_str(),
            "user" | "user-early" | "user-light" | "user-early-light"
        )
    {
        return Err(SessionUnavailable::NoGraphicalSession);
    }
    let user = properties.lookup_value("User", None).ok_or_else(invalid)?;
    if !user.is_type(glib::VariantTy::new("(uo)").map_err(|_| invalid())?) {
        return Err(invalid());
    }
    let actual_uid = user.child_value(0).get::<u32>().ok_or_else(invalid)?;
    if actual_uid != uid {
        return Err(SessionUnavailable::WrongUser);
    }
    let id = properties
        .lookup::<String>("Id")
        .map_err(|_| invalid())?
        .ok_or_else(invalid)?;
    if id.is_empty() {
        return Err(invalid());
    }
    Ok(GraphicalSession {
        id,
        path: path.to_owned(),
        uid,
        session_type: kind,
        can_lock: properties
            .lookup::<bool>("CanLock")
            .map_err(|_| invalid())?
            .unwrap_or(false),
        active: properties
            .lookup::<bool>("Active")
            .map_err(|_| invalid())?
            .ok_or_else(invalid)?,
        locked: properties
            .lookup::<bool>("LockedHint")
            .map_err(|_| invalid())?
            .ok_or_else(invalid)?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn properties(kind: &str, uid: u32, locked: bool) -> glib::Variant {
        let dict = glib::VariantDict::new(None);
        dict.insert("Type", kind);
        dict.insert("Class", "user");
        dict.insert("Id", "session-one");
        dict.insert("Active", true);
        dict.insert("CanLock", true);
        dict.insert("LockedHint", locked);
        dict.insert(
            "User",
            (
                uid,
                glib::variant::ObjectPath::try_from("/org/freedesktop/login1/user/_1000").unwrap(),
            ),
        );
        glib::Variant::tuple_from_iter([dict.end()])
    }

    #[test]
    fn only_structured_same_user_graphical_sessions_qualify() {
        let path = "/org/freedesktop/login1/session/one";
        let session = parse_properties(path, 1000, &properties("wayland", 1000, true)).unwrap();
        assert!(session.active && session.locked);
        assert_eq!(session.path, path);
        assert_eq!(
            parse_properties(path, 1000, &properties("tty", 1000, false)),
            Err(SessionUnavailable::NoGraphicalSession)
        );
        assert_eq!(
            parse_properties(path, 1000, &properties("wayland", 1001, false)),
            Err(SessionUnavailable::WrongUser)
        );
        assert_eq!(
            parse_properties(path, 1000, &false.to_variant()),
            Err(SessionUnavailable::InvalidProperties)
        );
    }
}
