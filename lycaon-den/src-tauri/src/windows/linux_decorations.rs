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

/// Event carrying the desktop's layout after `button-layout` changes.
pub const LAYOUT_CHANGED_EVENT: &str = "linux-decoration-layout-changed";

/// The desktop's layout as the Settings portal last reported it, else the default.
#[tauri::command]
pub fn linux_decoration_layout() -> DecorationLayout {
    current_layout()
}

/// A layout from a raw `button-layout`; a value naming no control falls back to the default.
pub(crate) fn layout_from_raw(raw: &str) -> DecorationLayout {
    parse_button_layout(raw).unwrap_or_else(DecorationLayout::default_layout)
}

#[cfg(target_os = "linux")]
fn current_layout() -> DecorationLayout {
    super::linux_desktop_settings::portal::current_decoration_layout()
}

#[cfg(not(target_os = "linux"))]
fn current_layout() -> DecorationLayout {
    DecorationLayout::default_layout()
}

#[cfg(test)]
mod tests {
    use super::*;
    use DecorationControl::*;

    /// The drawn buttons and menu entries call these window operations from
    /// every window that draws its own chrome.
    #[test]
    fn window_capabilities_grant_the_drawn_chrome_operations() {
        let manifest = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        for capability in ["capabilities/default.json", "capabilities/items.json"] {
            let text = std::fs::read_to_string(manifest.join(capability)).expect("capability file");
            for permission in [
                "core:window:allow-minimize",
                "core:window:allow-toggle-maximize",
                "core:window:allow-close",
                "core:window:allow-set-fullscreen",
                "allow-linux-decoration-layout",
                "allow-linux-window-frame",
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
    fn a_changed_value_naming_no_control_draws_the_default() {
        assert_eq!(layout_from_raw("icon:"), DecorationLayout::default_layout());
        assert_eq!(layout_from_raw("close:"), portal(&[Close], &[]));
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
