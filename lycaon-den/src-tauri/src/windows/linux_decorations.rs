//! Window button layout for client-side decorations on Linux.
//!
//! The desktop publishes its layout as `org.gnome.desktop.wm.preferences
//! button-layout` through the Settings portal. The shell draws the buttons
//! the desktop lists, where it lists them, and falls back to the full set on
//! the right when no desktop answers.

use serde::{Deserialize, Serialize};

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

/// What a click on the title bar does, as GTK's own client-side title bars
/// apply `action-*-click-titlebar`: an unrecognized action maximizes.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum TitlebarAction {
    ToggleMaximize,
    Minimize,
    Lower,
    Menu,
    None,
}

impl TitlebarAction {
    pub fn parse(raw: &str) -> Self {
        match raw.trim() {
            "none" => Self::None,
            "lower" => Self::Lower,
            "minimize" => Self::Minimize,
            "menu" => Self::Menu,
            _ => Self::ToggleMaximize,
        }
    }
}

/// The title bar's double, middle, and right click actions.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
pub struct TitlebarActions {
    pub double_click: TitlebarAction,
    pub middle_click: TitlebarAction,
    pub right_click: TitlebarAction,
}

impl Default for TitlebarActions {
    /// GTK's defaults when the desktop states none.
    fn default() -> Self {
        Self {
            double_click: TitlebarAction::ToggleMaximize,
            middle_click: TitlebarAction::None,
            right_click: TitlebarAction::Menu,
        }
    }
}

/// Controls on each side of the title bar, in display order, and what clicks on it do.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct DecorationLayout {
    pub left: Vec<DecorationControl>,
    pub right: Vec<DecorationControl>,
    pub source: DecorationLayoutSource,
    pub actions: TitlebarActions,
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
            actions: TitlebarActions::default(),
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
        actions: TitlebarActions::default(),
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
                "allow-linux-titlebar-action",
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
            actions: TitlebarActions::default(),
        }
    }

    #[test]
    fn titlebar_actions_follow_gtk_including_its_maximize_fallback() {
        assert_eq!(TitlebarAction::parse("none"), TitlebarAction::None);
        assert_eq!(TitlebarAction::parse("lower"), TitlebarAction::Lower);
        assert_eq!(TitlebarAction::parse("minimize"), TitlebarAction::Minimize);
        assert_eq!(TitlebarAction::parse("menu"), TitlebarAction::Menu);
        for raw in ["toggle-maximize", "toggle-maximize-horizontally", "toggle-shade", ""] {
            assert_eq!(TitlebarAction::parse(raw), TitlebarAction::ToggleMaximize, "{raw}");
        }
        let actions = serde_json::to_value(TitlebarActions::default()).unwrap();
        assert_eq!(actions, serde_json::json!({"double_click": "toggle-maximize", "middle_click": "none", "right_click": "menu"}));
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
    fn the_wire_shape_uses_lowercase_names() {
        let json = serde_json::to_string(&parse_button_layout("appmenu:close").unwrap()).unwrap();
        assert_eq!(
            json,
            r#"{"left":["appmenu"],"right":["close"],"source":"portal","actions":{"double_click":"toggle-maximize","middle_click":"none","right_click":"menu"}}"#
        );
    }
}
