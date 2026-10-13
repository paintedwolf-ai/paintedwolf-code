use std::{cell::RefCell, collections::HashMap, rc::Rc, time::Instant};

use gtk::{gio, glib, prelude::*};

use super::identity::Delivered;

const SERVICE: &str = "org.freedesktop.Notifications";
const PATH: &str = "/org/freedesktop/Notifications";
const BUS: &str = "org.freedesktop.DBus";
const BUS_PATH: &str = "/org/freedesktop/DBus";
const APPLICATION_NAME: &str = "Painted Wolf Code";
/// Desktop entry id installed by the Linux packages.
pub(super) const DESKTOP_ENTRY: &str = "ai.paintedwolf.code";
const SHELL_NOTIFICATIONS: &str = "org.gnome.Shell.Notifications";
const BANNER_SCHEMA: &str = "org.gnome.desktop.notifications";
const BANNER_KEY: &str = "show-banners";
const DO_NOT_DISTURB_MODE: &str = "do-not-disturb";

/// Properties a notification server exposes while it withholds banners.
/// A boolean is the paused state; a mode list is paused while it names
/// do-not-disturb.
const INHIBITION_PROPERTIES: &[(&str, &str, &str)] = &[
    (PATH, SERVICE, "Inhibited"),
    (PATH, "org.dunstproject.cmd0", "paused"),
    ("/fr/emersion/Mako", "fr.emersion.Mako", "Modes"),
];

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum Outcome {
    Delivered,
    /// The service accepted the notification but is withholding banners.
    Inhibited,
}

impl Outcome {
    pub(super) fn as_str(self) -> &'static str {
        match self {
            Outcome::Delivered => "delivered",
            Outcome::Inhibited => "inhibited",
        }
    }
}

pub(super) struct Delivery {
    connection: gio::DBusConnection,
    owner: String,
    capabilities: Vec<String>,
    delivered: Rc<RefCell<Delivered>>,
    subscriptions: Vec<gio::SignalSubscriptionId>,
}

impl Delivery {
    pub(super) fn new(
        connection: gio::DBusConnection,
        activated: impl Fn(Option<String>, Option<String>) + 'static,
        deadline: Instant,
    ) -> Result<Self, String> {
        let owner = resolve_owner(&connection, deadline)?;
        let reply = call(&connection, &owner, "GetCapabilities", None, deadline)?;
        let (capabilities,) = reply
            .get::<(Vec<String>,)>()
            .ok_or("Invalid desktop notification capabilities")?;
        if capabilities.len() > 64 || capabilities.iter().any(|capability| capability.len() > 128) {
            return Err("Desktop notification capabilities exceeded their limit".into());
        }
        let can_activate = capabilities
            .iter()
            .any(|capability| capability == "actions");
        let delivered = Rc::new(RefCell::new(Delivered::default()));
        let mut subscriptions = Vec::new();
        let entries = delivered.clone();
        subscriptions.push(connection.signal_subscribe(
            Some(&owner),
            Some(SERVICE),
            None,
            Some(PATH),
            None,
            gio::DBusSignalFlags::NONE,
            move |_, _, _, _, signal, parameters| match signal {
                "NotificationClosed" => {
                    if let Some((id, _reason)) = parameters.get::<(u32, u32)>() {
                        entries.borrow_mut().closed(id);
                    }
                }
                "ActivationToken" => {
                    if let Some((id, token)) = parameters.get::<(u32, String)>() {
                        entries.borrow_mut().token(id, token);
                    }
                }
                "ActionInvoked" => {
                    if !can_activate {
                        return;
                    }
                    if let Some((id, action)) = parameters.get::<(u32, String)>() {
                        let activation = entries.borrow_mut().activate(id, &action);
                        if let Some((session_id, token)) = activation {
                            activated(session_id, token);
                        }
                    }
                }
                _ => {}
            },
        ));
        let entries = delivered.clone();
        subscriptions.push(connection.signal_subscribe(
            Some(BUS),
            Some(BUS),
            Some("NameOwnerChanged"),
            Some(BUS_PATH),
            Some(SERVICE),
            gio::DBusSignalFlags::NONE,
            move |_, _, _, _, _, parameters| {
                if let Some((name, old, new)) = parameters.get::<(String, String, String)>() {
                    if name == SERVICE && old != new {
                        entries.borrow_mut().clear();
                    }
                }
            },
        ));
        Ok(Self {
            connection,
            owner,
            capabilities,
            delivered,
            subscriptions,
        })
    }

    pub(super) fn current(&self, deadline: Instant) -> bool {
        !self.connection.is_closed()
            && current_owner(&self.connection, deadline).is_ok_and(|owner| owner == self.owner)
    }

    pub(super) fn send(
        &self,
        title: String,
        body: String,
        session_id: Option<String>,
        deadline: Instant,
    ) -> Result<Outcome, String> {
        if title.len() > 4096
            || body.len() > 32 << 10
            || title.contains('\0')
            || body.contains('\0')
        {
            return Err("The notification content exceeded its limit".into());
        }
        let session_id = session_id.filter(|id| !id.is_empty());
        if session_id
            .as_ref()
            .is_some_and(|id| id.len() > 128 || id.contains(['\0', '\r', '\n']))
        {
            return Err("The notification chat identifier is invalid".into());
        }
        let replacement = self.delivered.borrow().replacement(session_id.as_deref());
        self.delivered.borrow().admit(replacement)?;
        let body = notification_body(&body, &self.capabilities);
        let hints = HashMap::from([
            ("desktop-entry", DESKTOP_ENTRY.to_variant()),
            ("suppress-sound", true.to_variant()),
        ]);
        let actions = if self
            .capabilities
            .iter()
            .any(|capability| capability == "actions")
        {
            vec!["default", "Open chat"]
        } else {
            Vec::new()
        };
        let parameters = (
            APPLICATION_NAME,
            replacement,
            "",
            title,
            body,
            actions,
            hints,
            -1i32,
        )
            .to_variant();
        let reply = call(
            &self.connection,
            &self.owner,
            "Notify",
            Some(&parameters),
            deadline,
        )?;
        let (id,) = reply
            .get::<(u32,)>()
            .ok_or("Invalid desktop notification identifier")?;
        self.delivered
            .borrow_mut()
            .record(id, replacement, session_id)?;
        Ok(if self.inhibited(deadline) {
            Outcome::Inhibited
        } else {
            Outcome::Delivered
        })
    }

    pub(super) fn cancel(&self, sessions: &[String], deadline: Instant) -> Result<(), String> {
        if sessions.len() > 256 || sessions.iter().any(|id| id.len() > 128) {
            return Err("The notification cancellation exceeded its limit".into());
        }
        let ids = self.delivered.borrow_mut().cancel(sessions);
        let mut failure = None;
        for id in ids {
            if let Err(error) = call(
                &self.connection,
                &self.owner,
                "CloseNotification",
                Some(&(id,).to_variant()),
                deadline,
            ) {
                failure = Some(error);
            }
        }
        failure.map_or(Ok(()), Err)
    }

    /// Whether the service currently withholds banners. An unknown state is not
    /// inhibition.
    fn inhibited(&self, deadline: Instant) -> bool {
        for (path, interface, property) in INHIBITION_PROPERTIES {
            let Ok(remaining) = timeout(deadline) else {
                return false;
            };
            let Ok(reply) = self.connection.call_sync(
                Some(&self.owner),
                path,
                "org.freedesktop.DBus.Properties",
                "Get",
                Some(&(*interface, *property).to_variant()),
                None,
                gio::DBusCallFlags::NO_AUTO_START,
                remaining,
                gio::Cancellable::NONE,
            ) else {
                continue;
            };
            let Some((value,)) = reply.get::<(glib::Variant,)>() else {
                continue;
            };
            if let Some(paused) = value.get::<bool>() {
                return paused;
            }
            if let Some(modes) = value.get::<Vec<String>>() {
                return modes.iter().any(|mode| mode == DO_NOT_DISTURB_MODE);
            }
        }
        self.shell_banners_hidden(deadline)
    }

    /// GNOME Shell's server keeps its banner policy in a settings key rather
    /// than a bus property.
    fn shell_banners_hidden(&self, deadline: Instant) -> bool {
        let shell_owner = name_owner(&self.connection, SHELL_NOTIFICATIONS, deadline);
        if shell_owner.as_deref() != Ok(self.owner.as_str()) {
            return false;
        }
        let Some(source) = gio::SettingsSchemaSource::default() else {
            return false;
        };
        let Some(schema) = source.lookup(BANNER_SCHEMA, true) else {
            return false;
        };
        if !schema.has_key(BANNER_KEY) {
            return false;
        }
        let settings = gio::Settings::new_full(&schema, None::<&gio::SettingsBackend>, None);
        !settings.boolean(BANNER_KEY)
    }
}

impl Drop for Delivery {
    fn drop(&mut self) {
        self.delivered.borrow_mut().clear();
        for subscription in self.subscriptions.drain(..) {
            self.connection.signal_unsubscribe(subscription);
        }
        let context = glib::MainContext::ref_thread_default();
        let _ = context.block_on(glib::future_with_timeout(
            std::time::Duration::from_millis(500),
            self.connection.close_future(),
        ));
    }
}

/// The running owner, or the owner started from an installed service file.
fn resolve_owner(connection: &gio::DBusConnection, deadline: Instant) -> Result<String, String> {
    if let Ok(owner) = current_owner(connection, deadline) {
        return Ok(owner);
    }
    let reply = bus_call(
        connection,
        "ListActivatableNames",
        None,
        timeout(deadline)?,
    )?;
    let (names,) = reply
        .get::<(Vec<String>,)>()
        .ok_or("Invalid desktop service listing")?;
    if !names.iter().any(|name| name == SERVICE) {
        return Err("No desktop notification service is installed".into());
    }
    bus_call(
        connection,
        "StartServiceByName",
        Some(&(SERVICE, 0u32).to_variant()),
        remaining(deadline)?,
    )?;
    current_owner(connection, deadline)
}

fn current_owner(connection: &gio::DBusConnection, deadline: Instant) -> Result<String, String> {
    name_owner(connection, SERVICE, deadline)
}

fn name_owner(
    connection: &gio::DBusConnection,
    name: &str,
    deadline: Instant,
) -> Result<String, String> {
    let reply = bus_call(
        connection,
        "GetNameOwner",
        Some(&(name,).to_variant()),
        timeout(deadline)?,
    )?;
    let (owner,) = reply
        .get::<(String,)>()
        .ok_or("Invalid desktop service owner")?;
    Ok(owner)
}

fn bus_call(
    connection: &gio::DBusConnection,
    method: &str,
    parameters: Option<&glib::Variant>,
    timeout_ms: i32,
) -> Result<glib::Variant, String> {
    connection
        .call_sync(
            Some(BUS),
            BUS_PATH,
            BUS,
            method,
            parameters,
            None,
            gio::DBusCallFlags::NO_AUTO_START,
            timeout_ms,
            gio::Cancellable::NONE,
        )
        .map_err(|error| error.to_string())
}

fn call(
    connection: &gio::DBusConnection,
    owner: &str,
    method: &str,
    parameters: Option<&glib::Variant>,
    deadline: Instant,
) -> Result<glib::Variant, String> {
    connection
        .call_sync(
            Some(owner),
            PATH,
            SERVICE,
            method,
            parameters,
            None,
            gio::DBusCallFlags::NO_AUTO_START,
            timeout(deadline)?,
            gio::Cancellable::NONE,
        )
        .map_err(|error| error.to_string())
}

/// Service calls are capped well below the request deadline so one slow call
/// cannot consume the whole request.
fn timeout(deadline: Instant) -> Result<i32, String> {
    remaining(deadline).map(|remaining| remaining.min(500))
}

fn remaining(deadline: Instant) -> Result<i32, String> {
    let remaining = deadline
        .saturating_duration_since(Instant::now())
        .as_millis()
        .min(i32::MAX as u128);
    if remaining == 0 {
        return Err("The desktop notification request timed out".into());
    }
    Ok(remaining as i32)
}

fn notification_body(body: &str, capabilities: &[String]) -> String {
    if !capabilities.iter().any(|capability| capability == "body") {
        return String::new();
    }
    if capabilities
        .iter()
        .any(|capability| capability == "body-markup")
    {
        glib::markup_escape_text(body).to_string()
    } else {
        body.into()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn notification_body_respects_server_markup_and_body_capabilities() {
        let content = "<secret> & \"text\"";
        assert_eq!(notification_body(content, &[]), "");
        assert_eq!(notification_body(content, &["body".into()]), content);
        assert_eq!(
            notification_body(content, &["body".into(), "body-markup".into()]),
            "&lt;secret&gt; &amp; &quot;text&quot;"
        );
    }

    #[test]
    fn request_deadline_bounds_each_service_call() {
        assert!(timeout(Instant::now()).is_err());
        assert!(remaining(Instant::now()).is_err());
        let deadline = Instant::now() + std::time::Duration::from_secs(10);
        assert_eq!(timeout(deadline).unwrap(), 500);
        assert!(remaining(deadline).unwrap() > 9_000);
    }

    #[test]
    fn outcomes_name_delivery_and_inhibition() {
        assert_eq!(Outcome::Delivered.as_str(), "delivered");
        assert_eq!(Outcome::Inhibited.as_str(), "inhibited");
    }
}

#[cfg(test)]
#[path = "delivery_tests.rs"]
mod native_tests;
