use super::*;

/// Replays a portal snapshot and `SettingChanged` signals into the state.
struct FixtureBus {
    snapshot: SettingsSnapshot,
    state: DesktopSettingsState,
}

impl FixtureBus {
    fn new() -> Self {
        Self {
            snapshot: SettingsSnapshot::new(),
            state: DesktopSettingsState::new(),
        }
    }

    fn with(mut self, namespace: &str, key: &str, value: SettingValue) -> Self {
        self.snapshot
            .entry(namespace.to_string())
            .or_default()
            .insert(key.to_string(), value);
        self
    }

    fn connect(&mut self) -> Vec<Emission> {
        self.state.replace_snapshot(self.snapshot.clone())
    }

    fn setting_changed(&mut self, namespace: &str, key: &str, value: SettingValue) -> Vec<Emission> {
        self.state.apply_change(namespace, key, value)
    }
}

fn gnome_defaults() -> FixtureBus {
    FixtureBus::new()
        .with(APPEARANCE_NS, "contrast", SettingValue::U32(0))
        .with(APPEARANCE_NS, "color-scheme", SettingValue::U32(0))
        .with(APPEARANCE_NS, "reduced-motion", SettingValue::U32(0))
        .with(
            APPEARANCE_NS,
            "accent-color",
            SettingValue::Rgb(0.207_843, 0.517_647, 0.894_118),
        )
        .with(GNOME_INTERFACE_NS, "text-scaling-factor", SettingValue::F64(1.0))
        .with(GNOME_INTERFACE_NS, "enable-animations", SettingValue::Bool(true))
        .with(GNOME_INTERFACE_NS, "gtk-theme", SettingValue::Str("Adwaita".into()))
        .with(GNOME_A11Y_NS, "high-contrast", SettingValue::Bool(false))
}

#[test]
fn gnome_snapshot_reads_every_fact_the_desktop_exposes() {
    let facts = DesktopSettingsFacts::from_snapshot(&gnome_defaults().snapshot);
    assert_eq!(
        facts,
        DesktopSettingsFacts {
            text_scaling_factor: Some(1.0),
            increase_contrast: Some(false),
            accent: Some("#3584e4".into()),
        }
    );
}

#[test]
fn empty_snapshot_exposes_no_preference() {
    let facts = DesktopSettingsFacts::from_snapshot(&SettingsSnapshot::new());
    assert_eq!(facts, DesktopSettingsFacts::default());
    let state = DesktopSettingsState::new();
    assert_eq!(state.text_size_payload().category, "default");
    assert_eq!(state.text_size_payload().text_scale, 1.0);
    assert!(!state.appearance_payload().increase_contrast);
    assert_eq!(state.appearance_payload().accent, None);
}

#[test]
fn contrast_follows_either_portal_or_gnome_high_contrast() {
    let portal = FixtureBus::new().with(APPEARANCE_NS, "contrast", SettingValue::U32(1));
    assert_eq!(
        DesktopSettingsFacts::from_snapshot(&portal.snapshot).increase_contrast,
        Some(true)
    );
    let gnome = FixtureBus::new()
        .with(APPEARANCE_NS, "contrast", SettingValue::U32(0))
        .with(GNOME_A11Y_NS, "high-contrast", SettingValue::Bool(true));
    assert_eq!(
        DesktopSettingsFacts::from_snapshot(&gnome.snapshot).increase_contrast,
        Some(true)
    );
}

#[test]
fn out_of_range_portal_accent_means_no_preference() {
    let bus = FixtureBus::new().with(APPEARANCE_NS, "accent-color", SettingValue::Rgb(-1.0, 0.0, 0.0));
    assert_eq!(DesktopSettingsFacts::from_snapshot(&bus.snapshot).accent, None);
}

#[test]
fn kde_accent_bytes_fill_in_when_the_portal_has_none() {
    let bus = FixtureBus::new().with(KDE_GENERAL_NS, "AccentColor", SettingValue::Str("61,174,233".into()));
    assert_eq!(
        DesktopSettingsFacts::from_snapshot(&bus.snapshot).accent,
        Some("#3daee9".into())
    );
    let scheme_default = FixtureBus::new().with(KDE_GENERAL_NS, "AccentColor", SettingValue::Str(String::new()));
    assert_eq!(DesktopSettingsFacts::from_snapshot(&scheme_default.snapshot).accent, None);
}

#[test]
fn text_scaling_factor_is_reported_without_rescaling_the_root() {
    let mut bus = gnome_defaults();
    bus.connect();
    bus.setting_changed(GNOME_INTERFACE_NS, "text-scaling-factor", SettingValue::F64(1.5));
    let payload = bus.state.text_size_payload();
    assert_eq!(payload.category, TEXT_SCALING_CATEGORY);
    assert_eq!(payload.text_scale, 1.0);
    assert_eq!(bus.state.facts().text_scaling_factor, Some(1.5));
}

#[test]
fn non_positive_text_scaling_factor_is_not_a_fact() {
    let bus = FixtureBus::new().with(GNOME_INTERFACE_NS, "text-scaling-factor", SettingValue::F64(0.0));
    assert_eq!(DesktopSettingsFacts::from_snapshot(&bus.snapshot).text_scaling_factor, None);
}

#[test]
fn first_read_emits_both_payloads_at_revision_one() {
    let mut bus = gnome_defaults();
    let emissions = bus.connect();
    assert_eq!(emissions.len(), 2);
    assert!(matches!(
        &emissions[0],
        Emission::TextSize(payload) if payload.revision == 1 && payload.category == TEXT_SCALING_CATEGORY
    ));
    assert!(matches!(
        &emissions[1],
        Emission::Appearance(payload) if payload.revision == 1 && payload.accent.as_deref() == Some("#3584e4")
    ));
}

#[test]
fn setting_changed_emits_only_the_payload_whose_content_moved() {
    let mut bus = gnome_defaults();
    bus.connect();
    let emissions = bus.setting_changed(GNOME_A11Y_NS, "high-contrast", SettingValue::Bool(true));
    assert_eq!(emissions.len(), 1);
    assert!(matches!(
        &emissions[0],
        Emission::Appearance(payload) if payload.increase_contrast && payload.revision == 2
    ));
    assert_eq!(bus.state.text_size_payload().revision, 1);
}

#[test]
fn unrelated_and_repeated_changes_emit_nothing() {
    let mut bus = gnome_defaults();
    bus.connect();
    assert!(bus
        .setting_changed(GNOME_INTERFACE_NS, "gtk-theme", SettingValue::Str("Adwaita-dark".into()))
        .is_empty());
    assert!(bus
        .setting_changed(APPEARANCE_NS, "contrast", SettingValue::U32(0))
        .is_empty());
    assert!(bus
        .setting_changed(APPEARANCE_NS, "color-scheme", SettingValue::U32(1))
        .is_empty());
    assert_eq!(bus.state.appearance_payload().revision, 1);
}

#[test]
fn losing_the_accent_preference_clears_it() {
    let mut bus = gnome_defaults();
    bus.connect();
    let emissions = bus.setting_changed(APPEARANCE_NS, "accent-color", SettingValue::Rgb(2.0, 2.0, 2.0));
    assert!(matches!(
        &emissions[..],
        [Emission::Appearance(payload)] if payload.accent.is_none() && payload.revision == 2
    ));
}

#[test]
fn transparency_has_no_linux_source_and_stays_off() {
    let mut bus = gnome_defaults();
    bus.connect();
    assert!(!bus.state.appearance_payload().reduce_transparency);
}
