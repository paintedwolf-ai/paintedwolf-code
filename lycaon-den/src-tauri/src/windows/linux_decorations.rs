//! Window button layout for client-side decorations on Linux.
//!
//! The desktop publishes its layout as `org.gnome.desktop.wm.preferences
//! button-layout` through the Settings portal. The shell draws the buttons
//! the desktop lists, where it lists them, and falls back to the full set on
//! the right when no desktop answers.

use serde::Serialize;

/// One control the desktop places in a title bar.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum DecorationControl {
    Appmenu,
    Minimize,
    Maximize,
    Close,
}

impl DecorationControl {
    fn parse(token: &str) -> Option<Self> {
        match token.trim() {
            "appmenu" => Some(Self::Appmenu),
            "minimize" => Some(Self::Minimize),
            "maximize" => Some(Self::Maximize),
            "close" => Some(Self::Close),
            _ => None,
        }
    }
}

/// Where the fact came from, so the shell can report a desktop that answered.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum DecorationLayoutSource {
    Portal,
    Default,
}

/// Controls on each side of the title bar, in display order.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct DecorationLayout {
    pub left: Vec<DecorationControl>,
    pub right: Vec<DecorationControl>,
    pub source: DecorationLayoutSource,
}

impl DecorationLayout {
    /// Minimize, maximize, and close on the right.
    pub fn default_layout() -> Self {
        Self {
            left: Vec::new(),
            right: vec![
                DecorationControl::Minimize,
                DecorationControl::Maximize,
                DecorationControl::Close,
            ],
            source: DecorationLayoutSource::Default,
        }
    }
}

/// Parses a `button-layout` value such as `appmenu:minimize,maximize,close`.
///
/// Tokens the shell does not draw (`icon`, `spacer`) are dropped; a repeated
/// control keeps its first position. A value naming no control at all is not
/// a layout, so the default stands.
pub fn parse_button_layout(raw: &str) -> Option<DecorationLayout> {
    let (left, right) = match raw.split_once(':') {
        Some((left, right)) => (left, right),
        None => ("", raw),
    };
    let mut seen = Vec::new();
    let mut side = |spec: &str| -> Vec<DecorationControl> {
        spec.split(',')
            .filter_map(DecorationControl::parse)
            .filter(|control| {
                if seen.contains(control) {
                    return false;
                }
                seen.push(*control);
                true
            })
            .collect()
    };
    let left = side(left);
    let right = side(right);
    if left.is_empty() && right.is_empty() {
        return None;
    }
    Some(DecorationLayout {
        left,
        right,
        source: DecorationLayoutSource::Portal,
    })
}

/// The desktop's layout when the Settings portal exposes one, else the default.
#[tauri::command]
pub async fn linux_decoration_layout() -> DecorationLayout {
    tauri::async_runtime::spawn_blocking(current_layout)
        .await
        .unwrap_or_else(|_| DecorationLayout::default_layout())
}

#[cfg(target_os = "linux")]
fn current_layout() -> DecorationLayout {
    match portal::read_button_layout() {
        Ok(raw) => parse_button_layout(&raw).unwrap_or_else(DecorationLayout::default_layout),
        Err(reason) => {
            eprintln!("window button layout unavailable: {reason}; drawing the default");
            DecorationLayout::default_layout()
        }
    }
}

#[cfg(not(target_os = "linux"))]
fn current_layout() -> DecorationLayout {
    DecorationLayout::default_layout()
}

#[cfg(target_os = "linux")]
mod portal {
    use gtk::glib::{self, ToVariant, VariantTy};
    use gtk::gio;

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
        // The portal wraps the setting's own variant in a second variant.
        let mut value: glib::Variant = reply.child_value(0);
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
}

#[cfg(test)]
mod tests {
    use super::*;
    use DecorationControl::*;

    /// The drawn buttons, menu entries and resize zones call these window
    /// operations from every window that draws its own chrome.
    #[test]
    fn window_capabilities_grant_the_drawn_chrome_operations() {
        let manifest = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        for capability in ["capabilities/default.json", "capabilities/items.json"] {
            let text = std::fs::read_to_string(manifest.join(capability)).expect("capability file");
            for permission in [
                "core:window:allow-minimize",
                "core:window:allow-toggle-maximize",
                "core:window:allow-close",
                "core:window:allow-start-resize-dragging",
                "core:window:allow-set-fullscreen",
                "allow-linux-decoration-layout",
            ] {
                assert!(
                    text.contains(&format!("\"{permission}\"")),
                    "{capability} lacks {permission}"
                );
            }
        }
    }

    fn portal(left: &[DecorationControl], right: &[DecorationControl]) -> DecorationLayout {
        DecorationLayout {
            left: left.to_vec(),
            right: right.to_vec(),
            source: DecorationLayoutSource::Portal,
        }
    }

    #[test]
    fn gnome_default_keeps_only_the_close_button_and_the_app_menu_on_the_left() {
        assert_eq!(
            parse_button_layout("appmenu:close"),
            Some(portal(&[Appmenu], &[Close]))
        );
    }

    #[test]
    fn full_right_layout_keeps_the_desktop_order() {
        assert_eq!(
            parse_button_layout(":minimize,maximize,close"),
            Some(portal(&[], &[Minimize, Maximize, Close]))
        );
        assert_eq!(
            parse_button_layout("close,maximize,minimize:"),
            Some(portal(&[Close, Maximize, Minimize], &[]))
        );
    }

    #[test]
    fn undrawn_tokens_blanks_and_repeats_are_dropped() {
        assert_eq!(
            parse_button_layout("icon, appmenu :spacer,minimize,,close,minimize"),
            Some(portal(&[Appmenu], &[Minimize, Close]))
        );
    }

    #[test]
    fn a_value_without_a_separator_is_the_right_side() {
        assert_eq!(
            parse_button_layout("minimize,close"),
            Some(portal(&[], &[Minimize, Close]))
        );
    }

    #[test]
    fn a_value_naming_no_control_is_not_a_layout() {
        assert_eq!(parse_button_layout(""), None);
        assert_eq!(parse_button_layout(":"), None);
        assert_eq!(parse_button_layout("icon:spacer"), None);
    }

    #[test]
    fn the_default_is_the_full_set_on_the_right_from_no_desktop() {
        let layout = DecorationLayout::default_layout();
        assert!(layout.left.is_empty());
        assert_eq!(layout.right, vec![Minimize, Maximize, Close]);
        assert_eq!(layout.source, DecorationLayoutSource::Default);
    }

    #[test]
    fn the_wire_shape_uses_lowercase_control_names() {
        let json = serde_json::to_string(&parse_button_layout("appmenu:close").unwrap()).unwrap();
        assert_eq!(
            json,
            r#"{"left":["appmenu"],"right":["close"],"source":"portal"}"#
        );
    }
}
