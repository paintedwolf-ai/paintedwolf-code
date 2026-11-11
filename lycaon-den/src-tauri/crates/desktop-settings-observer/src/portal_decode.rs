use desktop_settings_observer::{Setting, Status};
use glib::Variant;

// GIO exposes the stable GDBus error domain and codes through its FFI.
#[derive(Clone, Copy)]
pub(super) struct DbCode(pub(super) i32);

impl glib::error::ErrorDomain for DbCode {
    fn domain() -> glib::Quark {
        unsafe { glib::translate::from_glib(gio::ffi::g_dbus_error_quark()) }
    }
    fn code(self) -> i32 {
        self.0
    }
    fn from(code: i32) -> Option<Self> {
        Some(Self(code))
    }
}

pub(super) fn status(error: &glib::Error) -> Status {
    if error.matches(DbCode(gio::ffi::G_DBUS_ERROR_ACCESS_DENIED))
        || error.matches(DbCode(gio::ffi::G_DBUS_ERROR_AUTH_FAILED))
        || error.matches(gio::IOErrorEnum::PermissionDenied)
    {
        Status::Denied
    } else if error.matches(DbCode(gio::ffi::G_DBUS_ERROR_SERVICE_UNKNOWN))
        || error.matches(DbCode(gio::ffi::G_DBUS_ERROR_NAME_HAS_NO_OWNER))
        || error.matches(DbCode(gio::ffi::G_DBUS_ERROR_UNKNOWN_INTERFACE))
        || error.matches(DbCode(gio::ffi::G_DBUS_ERROR_UNKNOWN_METHOD))
    {
        Status::Missing
    } else if error.matches(DbCode(gio::ffi::G_DBUS_ERROR_DISCONNECTED)) {
        Status::Disconnected
    } else if error.matches(DbCode(gio::ffi::G_DBUS_ERROR_NO_REPLY))
        || error.matches(gio::IOErrorEnum::TimedOut)
    {
        Status::TimedOut
    } else {
        Status::Failed
    }
}

pub(super) fn text(v: &Variant, limit: usize) -> Option<String> {
    let value = v.str()?;
    (value.len() <= limit && !value.contains('\0')).then(|| value.to_owned())
}

pub(super) fn value(namespace: &str, key: &str, boxed: &Variant) -> Option<Setting> {
    let v = if boxed.type_().as_str() == "v" {
        boxed.as_variant()?
    } else {
        boxed.clone()
    };
    let setting = match (namespace, key) {
        ("org.freedesktop.appearance", "contrast") => Setting::Contrast(v.get()?),
        ("org.freedesktop.appearance", "accent-color") => {
            let (r, g, b) = v.get::<(f64, f64, f64)>()?;
            Setting::Accent([r, g, b])
        }
        ("org.gnome.desktop.interface", "text-scaling-factor") => Setting::TextScale(v.get()?),
        ("org.gnome.desktop.a11y.interface", "high-contrast") => Setting::HighContrast(v.get()?),
        ("org.kde.kdeglobals.General", "AccentColor") => Setting::KdeAccent(text(&v, 32)?),
        ("org.gnome.desktop.wm.preferences", "button-layout") => {
            Setting::ButtonLayout(text(&v, 256)?)
        }
        ("org.gnome.desktop.wm.preferences", "action-double-click-titlebar") => {
            Setting::TitlebarDoubleClick(text(&v, 32)?)
        }
        ("org.gnome.desktop.wm.preferences", "action-middle-click-titlebar") => {
            Setting::TitlebarMiddleClick(text(&v, 32)?)
        }
        ("org.gnome.desktop.wm.preferences", "action-right-click-titlebar") => {
            Setting::TitlebarRightClick(text(&v, 32)?)
        }
        _ => return None,
    };
    setting.valid().then_some(setting)
}

#[derive(Default)]
pub(super) struct Journal {
    pub(super) settings: Vec<Setting>,
    pending: Vec<(String, String, Option<Setting>)>,
    ready: bool,
    pub(super) dirty: bool,
}

impl Journal {
    pub(super) fn change(&mut self, namespace: &str, key: &str, setting: Option<Setting>) {
        if !known(namespace, key) {
            return;
        }
        if !self.ready {
            self.pending.retain(|(n, k, _)| n != namespace || k != key);
            self.pending.push((namespace.into(), key.into(), setting));
            return;
        }
        self.settings.retain(|s| s.identity() != (namespace, key));
        if let Some(setting) = setting {
            self.settings.push(setting);
        }
        self.dirty = true;
    }

    pub(super) fn snapshot(&mut self, reply: &Variant) -> bool {
        if reply.size() > 64 * 1024 || reply.child_value(0).n_children() > 64 {
            return false;
        }
        self.settings.clear();
        for entry in reply.child_value(0).iter() {
            if let Some(namespace) = entry.child_value(0).str() {
                if entry.child_value(1).n_children() > 128 {
                    return false;
                }
                for row in entry.child_value(1).iter() {
                    if let Some(key) = row.child_value(0).str() {
                        if let Some(setting) = value(namespace, key, &row.child_value(1)) {
                            self.settings
                                .retain(|old| old.identity() != setting.identity());
                            self.settings.push(setting);
                        }
                    }
                }
            }
        }
        self.ready = true;
        for (namespace, key, setting) in std::mem::take(&mut self.pending) {
            self.change(&namespace, &key, setting);
        }
        self.dirty = true;
        true
    }
}

fn known(namespace: &str, key: &str) -> bool {
    matches!(
        (namespace, key),
        ("org.freedesktop.appearance", "contrast" | "accent-color")
            | ("org.gnome.desktop.interface", "text-scaling-factor")
            | ("org.gnome.desktop.a11y.interface", "high-contrast")
            | ("org.kde.kdeglobals.General", "AccentColor")
            | (
                "org.gnome.desktop.wm.preferences",
                "button-layout"
                    | "action-double-click-titlebar"
                    | "action-middle-click-titlebar"
                    | "action-right-click-titlebar"
            )
    )
}
