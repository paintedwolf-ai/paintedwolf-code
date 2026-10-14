//! `button-layout` through `org.freedesktop.portal.Settings`.

use gtk::gio;
use gtk::glib::{self, ToVariant, Variant, VariantTy};

const PORTAL_NAME: &str = "org.freedesktop.portal.Desktop";
const PORTAL_PATH: &str = "/org/freedesktop/portal/desktop";
const SETTINGS_INTERFACE: &str = "org.freedesktop.portal.Settings";
const NAMESPACE: &str = "org.gnome.desktop.wm.preferences";
const KEY: &str = "button-layout";
const TIMEOUT_MS: i32 = 500;

/// Reads the raw `button-layout` string through `Settings.Read`.
pub(super) fn read_button_layout() -> Result<String, String> {
    // A private context: this runs on a blocking worker, not the GTK main thread.
    let connection = crate::linux_desktop::session_bus::connect(&glib::MainContext::new())?;
    let reply = connection
        .call_sync(
            Some(PORTAL_NAME),
            PORTAL_PATH,
            SETTINGS_INTERFACE,
            "Read",
            Some(&(NAMESPACE, KEY).to_variant()),
            Some(VariantTy::new("(v)").map_err(|error| error.to_string())?),
            gio::DBusCallFlags::NONE,
            TIMEOUT_MS,
            gio::Cancellable::NONE,
        )
        .map_err(|error| error.to_string())?;
    layout_string(reply.child_value(0))
}

/// Runs a main loop on this thread and reports every new `button-layout`.
pub(super) fn watch_button_layout(changed: impl Fn(&str) + 'static) -> Result<(), String> {
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| {
            let connection = crate::linux_desktop::session_bus::connect(&context)?;
            // The namespace is the signal's first argument, so the bus filters it.
            let _subscription = connection.signal_subscribe(
                Some(PORTAL_NAME),
                Some(SETTINGS_INTERFACE),
                Some("SettingChanged"),
                Some(PORTAL_PATH),
                Some(NAMESPACE),
                gio::DBusSignalFlags::NONE,
                move |_, _, _, _, _, parameters| {
                    if let Some(raw) = changed_button_layout(parameters) {
                        changed(&raw);
                    }
                },
            );
            glib::MainLoop::new(Some(&context), false).run();
            Ok(())
        })
        .map_err(|error| format!("main context: {error}"))?
}

/// The new value when a `SettingChanged(ssv)` signal names `button-layout`.
fn changed_button_layout(parameters: &Variant) -> Option<String> {
    if parameters.n_children() != 3 {
        return None;
    }
    if parameters.child_value(0).str()? != NAMESPACE || parameters.child_value(1).str()? != KEY {
        return None;
    }
    layout_string(parameters.child_value(2)).ok()
}

/// The portal wraps the setting's own variant in one or more `v` layers.
fn layout_string(mut value: Variant) -> Result<String, String> {
    while value.is_type(VariantTy::VARIANT) {
        value = value
            .as_variant()
            .ok_or_else(|| "empty settings variant".to_string())?;
    }
    value
        .str()
        .map(str::to_owned)
        .ok_or_else(|| format!("button-layout is not a string: {}", value.type_()))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn signal(namespace: &str, key: &str, value: Variant) -> Variant {
        (namespace, key, value).to_variant()
    }

    #[test]
    fn a_layout_change_reports_the_unwrapped_string() {
        let boxed = Variant::from_variant(&"appmenu:minimize,close".to_variant());
        assert_eq!(
            changed_button_layout(&signal(NAMESPACE, KEY, boxed)).as_deref(),
            Some("appmenu:minimize,close")
        );
    }

    #[test]
    fn other_keys_and_namespaces_are_ignored() {
        let value = || Variant::from_variant(&"close:".to_variant());
        assert_eq!(changed_button_layout(&signal(NAMESPACE, "theme", value())), None);
        assert_eq!(
            changed_button_layout(&signal("org.gnome.desktop.interface", KEY, value())),
            None
        );
    }

    #[test]
    fn a_non_string_value_is_not_a_layout() {
        let boxed = Variant::from_variant(&7u32.to_variant());
        assert_eq!(changed_button_layout(&signal(NAMESPACE, KEY, boxed)), None);
        assert!(layout_string(Variant::from_variant(&true.to_variant())).is_err());
    }
}
