use std::{cell::RefCell, collections::HashMap, rc::Rc, time::Instant};

use gtk::{gio, glib, prelude::*};

use super::identity::Delivered;

const SERVICE: &str = "org.freedesktop.Notifications";
const PATH: &str = "/org/freedesktop/Notifications";

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
        let owner = current_owner(&connection, deadline)?;
        let reply = call(&connection, &owner, "GetCapabilities", None, deadline)?;
        let (capabilities,) = reply
            .get::<(Vec<String>,)>()
            .ok_or("Invalid desktop notification capabilities")?;
        if capabilities.len() > 64 || capabilities.iter().any(|capability| capability.len() > 128) {
            return Err("Desktop notification capabilities exceeded their limit".into());
        }
        if !capabilities
            .iter()
            .any(|capability| capability == "actions")
        {
            return Err("The desktop notification service cannot activate chats".into());
        }
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
            Some("org.freedesktop.DBus"),
            Some("org.freedesktop.DBus"),
            Some("NameOwnerChanged"),
            Some("/org/freedesktop/DBus"),
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
    ) -> Result<(), String> {
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
            ("desktop-entry", "dev.paintedwolf.code".to_variant()),
            ("suppress-sound", true.to_variant()),
        ]);
        let parameters = (
            "Painted Wolf Code",
            replacement,
            "",
            title,
            body,
            vec!["default", "Open chat"],
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
            .record(id, replacement, session_id)
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
}

impl Drop for Delivery {
    fn drop(&mut self) {
        self.delivered.borrow_mut().clear();
        for subscription in self.subscriptions.drain(..) {
            self.connection.signal_unsubscribe(subscription);
        }
    }
}

fn current_owner(connection: &gio::DBusConnection, deadline: Instant) -> Result<String, String> {
    let remaining = timeout(deadline)?;
    let reply = connection
        .call_sync(
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "GetNameOwner",
            Some(&(SERVICE,).to_variant()),
            None,
            gio::DBusCallFlags::NO_AUTO_START,
            remaining,
            gio::Cancellable::NONE,
        )
        .map_err(|error| error.to_string())?;
    let (owner,) = reply
        .get::<(String,)>()
        .ok_or("Invalid desktop notification service owner")?;
    Ok(owner)
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

fn timeout(deadline: Instant) -> Result<i32, String> {
    let remaining = deadline
        .saturating_duration_since(Instant::now())
        .as_millis()
        .min(500);
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
        assert_eq!(
            timeout(Instant::now() + std::time::Duration::from_secs(10)).unwrap(),
            500
        );
    }
}

#[cfg(test)]
#[path = "delivery_tests.rs"]
mod native_tests;
