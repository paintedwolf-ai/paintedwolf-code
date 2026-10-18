//! Owns an asynchronous portal request until its final response.

use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use glib::variant::Handle;
use gtk::{gio, glib, prelude::*};
use tokio::sync::oneshot;

use super::{
    ServiceError, CALL_TIMEOUT_MS, OPEN_URI_INTERFACE, PORTAL_BUS_NAME, PORTAL_OBJECT_PATH,
};

const REQUEST_INTERFACE: &str = "org.freedesktop.portal.Request";
const RESPONSE_TIMEOUT: Duration = Duration::from_secs(120);
type Completion = Arc<Mutex<Option<oneshot::Sender<Result<(), ServiceError>>>>>;

fn finish(completion: &Completion, result: Result<(), ServiceError>) {
    if let Some(sender) = completion.lock().unwrap_or_else(|e| e.into_inner()).take() {
        let _ = sender.send(result);
    }
}

fn response(parameters: &glib::Variant) -> Result<(), ServiceError> {
    if parameters.type_().as_str() != "(ua{sv})" {
        return Err(ServiceError::Failed("Invalid portal response".into()));
    }
    match parameters.child_value(0).get::<u32>() {
        Some(0) => Ok(()),
        Some(1) => Err(ServiceError::Cancelled),
        Some(2) => Err(ServiceError::Failed(
            "The portal could not complete the request".into(),
        )),
        _ => Err(ServiceError::Failed("Unknown portal response".into())),
    }
}

struct Subscriptions {
    bus: gio::DBusConnection,
    signals: Vec<gio::SignalSubscriptionId>,
    closed: Option<glib::SignalHandlerId>,
}

impl Drop for Subscriptions {
    fn drop(&mut self) {
        for subscription in self.signals.drain(..) {
            self.bus.signal_unsubscribe(subscription);
        }
        if let Some(handler) = self.closed.take() {
            self.bus.disconnect(handler);
        }
    }
}

/// Callers already run on a blocking command worker. This context only
/// dispatches the request's callbacks; it never runs the GTK event loop.
pub(super) fn run(
    bus: &gio::DBusConnection,
    invoke: impl FnOnce(&str, glib::Variant) -> Result<glib::Variant, ServiceError>,
) -> Result<(), ServiceError> {
    run_with_timeout(bus, RESPONSE_TIMEOUT, invoke)
}

fn run_with_timeout(
    bus: &gio::DBusConnection,
    timeout: Duration,
    invoke: impl FnOnce(&str, glib::Variant) -> Result<glib::Variant, ServiceError>,
) -> Result<(), ServiceError> {
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let owner = owner(bus)?;
            let token = format!("paintedwolf_{}", uuid::Uuid::new_v4().simple());
            let options = glib::VariantDict::new(None);
            options.insert("handle_token", token.as_str());
            let (sender, receiver) = oneshot::channel();
            let completion = Arc::new(Mutex::new(Some(sender)));
            let returned_path = Arc::new(Mutex::new(String::new()));
            let _subscriptions = subscribe(bus, &owner, returned_path.clone(), completion.clone());

            // Subscribe without a path filter before invocation. The private
            // context queues even an early response until the actual returned
            // path is known, including portals that do not honor handle_token.
            let reply = invoke(&owner, options.end())?;
            let path = request_path(&reply)?;
            *returned_path.lock().unwrap_or_else(|e| e.into_inner()) = path.clone();
            if bus.is_closed() {
                return Err(ServiceError::Failed(
                    "The desktop session bus disconnected".into(),
                ));
            }
            match context.block_on(glib::future_with_timeout(timeout, receiver)) {
                Ok(Ok(result)) => result,
                _ => {
                    close(bus, &owner, &path);
                    Err(ServiceError::Failed(
                        "The desktop portal request timed out".into(),
                    ))
                }
            }
        })
        .map_err(|error| ServiceError::Failed(error.to_string()))?
}

fn owner(bus: &gio::DBusConnection) -> Result<String, ServiceError> {
    let reply = bus
        .call_sync(
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "GetNameOwner",
            Some(&(PORTAL_BUS_NAME,).to_variant()),
            None,
            gio::DBusCallFlags::NONE,
            CALL_TIMEOUT_MS,
            gio::Cancellable::NONE,
        )
        .map_err(|error| ServiceError::from_call(&error))?;
    reply
        .get::<(String,)>()
        .map(|(owner,)| owner)
        .ok_or_else(|| ServiceError::Failed("Invalid desktop portal owner".into()))
}

fn request_path(reply: &glib::Variant) -> Result<String, ServiceError> {
    if reply.type_().as_str() == "(o)" {
        if let Some(path) = reply.child_value(0).str() {
            return Ok(path.to_string());
        }
    }
    Err(ServiceError::Failed(
        "Invalid desktop portal request handle".into(),
    ))
}

fn subscribe(
    bus: &gio::DBusConnection,
    owner: &str,
    path: Arc<Mutex<String>>,
    completion: Completion,
) -> Subscriptions {
    let result = completion.clone();
    let response = bus.signal_subscribe(
        Some(owner),
        Some(REQUEST_INTERFACE),
        Some("Response"),
        None,
        None,
        gio::DBusSignalFlags::NONE,
        move |_, _, received_path, _, _, parameters| {
            if received_path == *path.lock().unwrap_or_else(|e| e.into_inner()) {
                finish(&result, self::response(parameters));
            }
        },
    );
    let result = completion.clone();
    let watched_owner = owner.to_string();
    let owner_changed = bus.signal_subscribe(
        Some("org.freedesktop.DBus"),
        Some("org.freedesktop.DBus"),
        Some("NameOwnerChanged"),
        Some("/org/freedesktop/DBus"),
        Some(PORTAL_BUS_NAME),
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if let Some((_, old, new)) = parameters.get::<(String, String, String)>() {
                if old == watched_owner && new != old {
                    finish(
                        &result,
                        Err(ServiceError::Failed(
                            "The desktop portal disconnected".into(),
                        )),
                    );
                }
            }
        },
    );
    let closed = bus.connect_closed(move |_, _, _| {
        finish(
            &completion,
            Err(ServiceError::Failed(
                "The desktop session bus disconnected".into(),
            )),
        );
    });
    Subscriptions {
        bus: bus.clone(),
        signals: vec![response, owner_changed],
        closed: Some(closed),
    }
}

fn close(bus: &gio::DBusConnection, owner: &str, path: &str) {
    let _ = bus.call_sync(
        Some(owner),
        path,
        REQUEST_INTERFACE,
        "Close",
        None,
        None,
        gio::DBusCallFlags::NONE,
        CALL_TIMEOUT_MS,
        gio::Cancellable::NONE,
    );
}

pub(super) fn portal_open_uri(
    bus: &gio::DBusConnection,
    parent: &str,
    uri: &str,
) -> Result<(), ServiceError> {
    run(bus, |owner, options| {
        bus.call_sync(
            Some(owner),
            PORTAL_OBJECT_PATH,
            OPEN_URI_INTERFACE,
            "OpenURI",
            Some(&glib::Variant::tuple_from_iter([
                parent.to_variant(),
                uri.to_variant(),
                options,
            ])),
            Some(glib::VariantTy::new("(o)").expect("static signature")),
            gio::DBusCallFlags::NONE,
            CALL_TIMEOUT_MS,
            gio::Cancellable::NONE,
        )
        .map_err(|error| ServiceError::from_call(&error))
    })
}

/// `OpenFile` and `OpenDirectory` take the entry as a file descriptor, so the
/// portal opens exactly the entry this process could open. The request goes
/// out as a message because the reply carries no descriptors back.
pub(super) fn portal_open_fd(
    bus: &gio::DBusConnection,
    method: &str,
    parent: &str,
    path: &Path,
) -> Result<(), ServiceError> {
    let file = std::fs::File::open(path)
        .map_err(|error| ServiceError::Failed(format!("{}: {error}", path.display())))?;
    let fd_list = gio::UnixFDList::new();
    let index = fd_list
        .append(file)
        .map_err(|error| ServiceError::Failed(error.to_string()))?;
    run(bus, |owner, options| {
        let message = gio::DBusMessage::new_method_call(
            Some(owner),
            PORTAL_OBJECT_PATH,
            Some(OPEN_URI_INTERFACE),
            method,
        );
        message.set_body(&glib::Variant::tuple_from_iter([
            parent.to_variant(),
            Handle(index).to_variant(),
            options,
        ]));
        message.set_unix_fd_list(Some(&fd_list));
        let (reply, _serial) = bus
            .send_message_with_reply_sync(
                &message,
                gio::DBusSendMessageFlags::NONE,
                CALL_TIMEOUT_MS,
                gio::Cancellable::NONE,
            )
            .map_err(|error| ServiceError::from_call(&error))?;
        // A method return has nothing to convert; only an error reply names a cause.
        if reply.message_type() == gio::DBusMessageType::Error {
            return Err(ServiceError::from_error_name(
                reply.error_name().as_deref(),
                "the portal refused the request",
            ));
        }
        reply
            .body()
            .ok_or_else(|| ServiceError::Failed("Missing portal request handle".into()))
    })
}

#[cfg(test)]
mod tests;
