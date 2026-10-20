use super::*;
#[test]
fn typed_denial_cannot_become_missing() {
    assert_eq!(
        status(&glib::Error::new(
            DbCode(gio::ffi::G_DBUS_ERROR_ACCESS_DENIED),
            "diagnostic"
        )),
        Status::Denied
    );
    assert_eq!(
        status(&glib::Error::new(
            DbCode(gio::ffi::G_DBUS_ERROR_SERVICE_UNKNOWN),
            "same diagnostic"
        )),
        Status::Missing
    );
    assert_eq!(
        status(&glib::Error::new(
            DbCode(gio::ffi::G_DBUS_ERROR_FAILED),
            "same diagnostic"
        )),
        Status::Failed
    );
}
#[test]
fn settings_keys_and_variant_types_are_fixed() {
    assert_eq!(
        value(NAMESPACES[0], "contrast", &1u32.to_variant()),
        Some(Setting::Contrast(1))
    );
    assert!(value(NAMESPACES[0], "contrast", &"one".to_variant()).is_none());
    assert!(value(NAMESPACES[0], "execute-command", &"anything".to_variant()).is_none());
    assert!(value(NAMESPACES[0], "accent-color", &(2.0, 0.0, 0.0).to_variant()).is_none());
}
#[test]
fn startup_snapshot_replays_intervening_changes_and_invalidates_removed_fact() {
    let mut journal = Journal::default();
    journal.change(NAMESPACES[0], "contrast", Some(Setting::Contrast(1)));
    let namespaces = std::collections::BTreeMap::from([(
        NAMESPACES[0],
        std::collections::BTreeMap::from([("contrast", 0u32.to_variant())]),
    )]);
    journal.snapshot(&(namespaces,).to_variant());
    assert_eq!(journal.settings, vec![Setting::Contrast(1)]);
    journal.change(NAMESPACES[0], "contrast", None);
    assert!(journal.settings.is_empty());
    assert!(journal.dirty);
}

#[test]
fn text_preferences_are_bounded_before_copying() {
    assert!(text(&"x".repeat(257).to_variant(), 256).is_none());
    assert!(text(&"x\0y".to_variant(), 256).is_none());
    assert_eq!(
        text(&"left:right".to_variant(), 256),
        Some("left:right".into())
    );
}
