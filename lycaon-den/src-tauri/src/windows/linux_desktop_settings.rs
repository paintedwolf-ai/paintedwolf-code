//! Desktop appearance and text-scale facts from the Settings portal.
//!
//! A portal snapshot reduces to typed facts. A fact is absent when none of the
//! namespaces the desktop exposes carries it, so the app can report that
//! instead of inventing a preference.

use std::collections::BTreeMap;

use super::accessibility_text_size::{payload_for_category, TextSizePayload};
use super::system_appearance::{hex_color, SystemAppearancePayload};

#[cfg(target_os = "linux")]
pub(crate) mod portal;
#[cfg(test)]
mod tests;

pub const APPEARANCE_NS: &str = "org.freedesktop.appearance";
pub const GNOME_INTERFACE_NS: &str = "org.gnome.desktop.interface";
pub const GNOME_A11Y_NS: &str = "org.gnome.desktop.a11y.interface";
pub const KDE_GENERAL_NS: &str = "org.kde.kdeglobals.General";

/// Namespaces read at startup and followed through `SettingChanged`.
pub const NAMESPACES: [&str; 4] = [
    APPEARANCE_NS,
    GNOME_INTERFACE_NS,
    GNOME_A11Y_NS,
    KDE_GENERAL_NS,
];

/// Text-size category while the desktop exposes a text scaling factor.
pub const TEXT_SCALING_CATEGORY: &str = "linux-text-scaling-factor";

/// Portal values reduced to the shapes the facts read.
#[derive(Debug, Clone, PartialEq)]
pub enum SettingValue {
    Bool(bool),
    U32(u32),
    F64(f64),
    Str(String),
    Rgb(f64, f64, f64),
    Unsupported,
}

/// Namespace → key → value, as `ReadAll` returns it.
pub type SettingsSnapshot = BTreeMap<String, BTreeMap<String, SettingValue>>;

/// Preferences the desktop exposes; `None` means it exposes none for that concern.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct DesktopSettingsFacts {
    pub text_scaling_factor: Option<f64>,
    pub increase_contrast: Option<bool>,
    pub accent: Option<String>,
}

impl DesktopSettingsFacts {
    pub fn from_snapshot(snapshot: &SettingsSnapshot) -> Self {
        Self {
            text_scaling_factor: text_scaling_factor(snapshot),
            increase_contrast: any_true([
                match lookup(snapshot, APPEARANCE_NS, "contrast") {
                    Some(SettingValue::U32(level)) => Some(*level >= 1),
                    _ => None,
                },
                match lookup(snapshot, GNOME_A11Y_NS, "high-contrast") {
                    Some(SettingValue::Bool(on)) => Some(*on),
                    _ => None,
                },
            ]),
            accent: portal_accent(snapshot).or_else(|| kde_accent(snapshot)),
        }
    }
}

fn lookup<'a>(snapshot: &'a SettingsSnapshot, namespace: &str, key: &str) -> Option<&'a SettingValue> {
    snapshot.get(namespace)?.get(key)
}

/// `Some(true)` when any exposed signal asks for it; `None` when none is exposed.
fn any_true<const N: usize>(signals: [Option<bool>; N]) -> Option<bool> {
    signals
        .iter()
        .fold(None, |acc, signal| match (acc, signal) {
            (None, None) => None,
            (acc, signal) => Some(acc.unwrap_or(false) || signal.unwrap_or(false)),
        })
}

fn text_scaling_factor(snapshot: &SettingsSnapshot) -> Option<f64> {
    match lookup(snapshot, GNOME_INTERFACE_NS, "text-scaling-factor") {
        Some(SettingValue::F64(factor)) if factor.is_finite() && *factor > 0.0 => Some(*factor),
        _ => None,
    }
}

/// Components outside 0..=1 mean the desktop states no accent preference.
fn portal_accent(snapshot: &SettingsSnapshot) -> Option<String> {
    match lookup(snapshot, APPEARANCE_NS, "accent-color") {
        Some(SettingValue::Rgb(red, green, blue))
            if [red, green, blue].iter().all(|c| (0.0..=1.0).contains(*c)) =>
        {
            Some(hex_color(*red, *green, *blue))
        }
        _ => None,
    }
}

/// KDE stores the accent as `r,g,b` bytes; an empty string follows the color scheme.
fn kde_accent(snapshot: &SettingsSnapshot) -> Option<String> {
    let Some(SettingValue::Str(raw)) = lookup(snapshot, KDE_GENERAL_NS, "AccentColor") else {
        return None;
    };
    let channels: Vec<f64> = raw
        .split(',')
        .map(|part| part.trim().parse::<u8>().ok().map(|byte| f64::from(byte) / 255.0))
        .collect::<Option<Vec<f64>>>()?;
    match channels.as_slice() {
        [red, green, blue] => Some(hex_color(*red, *green, *blue)),
        _ => None,
    }
}

/// A payload whose content changed since the last emission.
#[derive(Debug, Clone, PartialEq)]
pub enum Emission {
    TextSize(TextSizePayload),
    Appearance(SystemAppearancePayload),
}

/// Current facts and the revisions both payloads carry.
#[derive(Debug)]
pub struct DesktopSettingsState {
    snapshot: SettingsSnapshot,
    facts: DesktopSettingsFacts,
    text_revision: u64,
    appearance_revision: u64,
}

impl DesktopSettingsState {
    pub const fn new() -> Self {
        Self {
            snapshot: BTreeMap::new(),
            facts: DesktopSettingsFacts {
                text_scaling_factor: None,
                increase_contrast: None,
                accent: None,
            },
            text_revision: 0,
            appearance_revision: 0,
        }
    }

    #[cfg(test)]
    pub fn facts(&self) -> &DesktopSettingsFacts {
        &self.facts
    }

    /// The web view applies the desktop factor as device scale, so the root
    /// rem must not multiply it again; the category still reports its presence.
    pub fn text_size_payload(&self) -> TextSizePayload {
        let mut payload = payload_for_category(if self.facts.text_scaling_factor.is_some() {
            TEXT_SCALING_CATEGORY
        } else {
            "default"
        });
        payload.revision = self.text_revision;
        payload
    }

    /// No Linux desktop exposes a transparency preference, so it stays off.
    pub fn appearance_payload(&self) -> SystemAppearancePayload {
        SystemAppearancePayload {
            increase_contrast: self.facts.increase_contrast.unwrap_or(false),
            reduce_transparency: false,
            accent: self.facts.accent.clone(),
            revision: self.appearance_revision,
        }
    }

    /// Replaces every namespace from a full read.
    pub fn replace_snapshot(&mut self, snapshot: SettingsSnapshot) -> Vec<Emission> {
        self.snapshot = snapshot;
        self.refresh()
    }

    /// Applies one `SettingChanged` signal.
    pub fn apply_change(&mut self, namespace: &str, key: &str, value: SettingValue) -> Vec<Emission> {
        self.snapshot
            .entry(namespace.to_string())
            .or_default()
            .insert(key.to_string(), value);
        self.refresh()
    }

    fn refresh(&mut self) -> Vec<Emission> {
        let previous_text = self.text_size_payload();
        let previous_appearance = self.appearance_payload();
        self.facts = DesktopSettingsFacts::from_snapshot(&self.snapshot);
        let mut emissions = Vec::new();
        let text = self.text_size_payload();
        if self.text_revision == 0 || text.category != previous_text.category {
            self.text_revision += 1;
            emissions.push(Emission::TextSize(self.text_size_payload()));
        }
        let appearance = self.appearance_payload();
        let appearance_changed = appearance.increase_contrast != previous_appearance.increase_contrast
            || appearance.accent != previous_appearance.accent;
        if self.appearance_revision == 0 || appearance_changed {
            self.appearance_revision += 1;
            emissions.push(Emission::Appearance(self.appearance_payload()));
        }
        emissions
    }
}
