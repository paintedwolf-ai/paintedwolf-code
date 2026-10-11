use super::*;
use crate::test_support::TempDir;

fn seed_file(dir: &std::path::Path, json: &str) {
    let mut doc = serde_json::from_str::<serde_json::Map<String, serde_json::Value>>(json)
        .expect("parse app-state seed");
    doc.remove("version");
    apply_app_state_patch_at(dir, &doc).expect("seed app-state");
}

fn read_file(dir: &std::path::Path) -> String {
    serde_json::to_string(&read_app_state_doc(dir).expect("read app-state"))
        .expect("serialize app-state")
}

fn map(pairs: &[(&str, serde_json::Value)]) -> serde_json::Map<String, serde_json::Value> {
    pairs
        .iter()
        .map(|(k, v)| ((*k).to_string(), v.clone()))
        .collect()
}

#[test]
fn patch_merges_slices_without_clobber() {
    let dir = TempDir::new("app-state-merge");
    seed_file(&dir, r#"{"version":1,"recents":[]}"#);
    let display = map(&[("display", serde_json::json!({"diffWordWrap": false}))]);
    patch_app_state_at(&dir, &display).expect("display patch");
    let layout = map(&[("layout", serde_json::json!({"navCollapsed": true}))]);
    let merged = patch_app_state_at(&dir, &layout).expect("layout patch");
    assert_eq!(merged["version"], 1);
    assert_eq!(merged["display"]["diffWordWrap"], false);
    assert_eq!(merged["layout"]["navCollapsed"], true);
    let on_disk: serde_json::Value = serde_json::from_str(&read_file(&dir)).expect("parse");
    assert_eq!(on_disk["display"]["diffWordWrap"], false);
    assert_eq!(on_disk["layout"]["navCollapsed"], true);
    assert_eq!(on_disk["version"], 1);
}

#[test]
fn patch_writes_only_the_named_slice() {
    let dir = TempDir::new("app-state-slice-isolation");
    seed_file(
        &dir,
        r#"{"version":1,"display":{"diffWordWrap":true},"layout":{"navCollapsed":false}}"#,
    );
    let layout_path = app_state_slices_dir(&dir).join(app_state_slice_name("layout"));
    let layout_before = fs::read(&layout_path).expect("read layout slice");

    patch_app_state_at(
        &dir,
        &map(&[("display", serde_json::json!({"diffWordWrap": false}))]),
    )
    .expect("patch display");

    assert_eq!(
        fs::read(&layout_path).expect("read layout slice after"),
        layout_before
    );
}

#[test]
fn patch_null_deletes_a_slice() {
    let dir = TempDir::new("app-state-null-delete");
    seed_file(
        &dir,
        r#"{"version":1,"display":{"diffWordWrap":true},"layout":{"navCollapsed":false}}"#,
    );
    let patch = map(&[("display", serde_json::Value::Null)]);
    let merged = patch_app_state_at(&dir, &patch).expect("null delete");
    assert!(merged.get("display").is_none());
    assert_eq!(merged["layout"]["navCollapsed"], false);
    assert_eq!(merged["version"], 1);
}

#[test]
fn patch_replaces_slice_wholesale() {
    let dir = TempDir::new("app-state-wholesale");
    seed_file(
        &dir,
        r#"{"shortcuts":{"overrides":{"a":"Mod+A","b":"Mod+B"}}}"#,
    );
    let patch = map(&[("shortcuts", serde_json::json!({"overrides":{"a":"Mod+A"}}))]);
    let merged = patch_app_state_at(&dir, &patch).expect("wholesale replace");
    assert_eq!(merged["version"], APP_STATE_VERSION);
    assert_eq!(merged["shortcuts"]["overrides"]["a"], "Mod+A");
    assert!(merged["shortcuts"]["overrides"].get("b").is_none());
}

#[test]
fn patch_refuses_version() {
    let dir = TempDir::new("app-state-refuse-version");
    let patch = map(&[("version", serde_json::json!(99))]);
    let err = patch_app_state_at(&dir, &patch).expect_err("version refused");
    assert_eq!(err, "app_state_version_immutable");
    assert!(!app_state_slices_dir(&dir).exists());

    seed_file(&dir, r#"{"version":1,"recents":[]}"#);
    let before = read_file(&dir);
    let err = patch_app_state_at(&dir, &patch).expect_err("version refused with file");
    assert_eq!(err, "app_state_version_immutable");
    assert_eq!(read_file(&dir), before);
}

#[test]
fn patch_accepts_unknown_top_level_key() {
    let dir = TempDir::new("app-state-unknown-key");
    let patch = map(&[("someFutureSlice", serde_json::json!({"x": 1}))]);
    let merged = patch_app_state_at(&dir, &patch).expect("unknown key");
    assert_eq!(merged["someFutureSlice"]["x"], 1);
    let on_disk: serde_json::Value = serde_json::from_str(&read_file(&dir)).expect("parse");
    assert_eq!(on_disk["someFutureSlice"]["x"], 1);
}

#[test]
fn unreadable_slice_does_not_hide_healthy_state() {
    let dir = TempDir::new("app-state-unreadable");
    seed_file(&dir, r#"{"version":1,"layout":{"navCollapsed":true}}"#);
    fs::write(app_state_slices_dir(&dir).join("broken.json"), "not json")
        .expect("write broken slice");
    let doc = read_app_state_doc(&dir).expect("read healthy slices");
    assert_eq!(doc["layout"]["navCollapsed"], true);
}

#[test]
fn mismatched_slice_envelope_is_ignored() {
    let dir = TempDir::new("app-state-mismatched-envelope");
    seed_file(&dir, r#"{"version":1,"layout":{"navCollapsed":true}}"#);
    fs::write(
        app_state_slices_dir(&dir).join(app_state_slice_name("debug")),
        r#"{"key":"layout","value":{"navCollapsed":false}}"#,
    )
    .expect("write mismatched slice");

    let doc = read_app_state_doc(&dir).expect("read healthy slices");
    assert_eq!(doc["layout"]["navCollapsed"], true);
    assert!(doc.get("debug").is_none());
}

#[test]
fn patch_leaves_no_temp_file() {
    let dir = TempDir::new("app-state-no-temp");
    seed_file(&dir, r#"{"version":1}"#);
    let before = read_file(&dir);
    let patch = map(&[("display", serde_json::json!({"diffWordWrap": true}))]);
    patch_app_state_at(&dir, &patch).expect("success");
    let slices = app_state_slices_dir(&dir);
    assert!(fs::read_dir(&slices)
        .expect("read slices")
        .all(|entry| !entry
            .expect("slice entry")
            .path()
            .to_string_lossy()
            .ends_with(".tmp")));

    // Make temporary-file creation fail.
    let fail_dir = TempDir::new("app-state-temp-as-dir");
    seed_file(&fail_dir, &before);
    let before_fail = read_file(&fail_dir);
    let name = app_state_slice_name("layout");
    let tmp = app_state_slices_dir(&fail_dir).join(format!("{name}.tmp"));
    fs::create_dir_all(app_state_slices_dir(&fail_dir)).expect("slice fixture dir");
    fs::create_dir(&tmp).expect("tmp as dir");
    let err = patch_app_state_at(
        &fail_dir,
        &map(&[("layout", serde_json::json!({"navCollapsed": true}))]),
    )
    .expect_err("create fails when tmp is a directory");
    assert!(!err.is_empty());
    assert_eq!(read_file(&fail_dir), before_fail);
    // Remove the fixture directory before checking for a file.
    if tmp.is_dir() {
        let _ = fs::remove_dir_all(&tmp);
    }
    assert!(!tmp.is_file());
}

#[test]
fn patch_creates_file_when_absent() {
    let dir = TempDir::new("app-state-create");
    let patch = map(&[("recents", serde_json::json!([]))]);
    let merged = patch_app_state_at(&dir, &patch).expect("create");
    assert!(app_state_slices_dir(&dir)
        .join(app_state_slice_name("recents"))
        .exists());
    assert_eq!(merged["version"], APP_STATE_VERSION);
    assert_eq!(merged["recents"], serde_json::json!([]));
    let on_disk: serde_json::Value = serde_json::from_str(&read_file(&dir)).expect("parse");
    assert_eq!(on_disk["version"], APP_STATE_VERSION);
    assert_eq!(on_disk["recents"], serde_json::json!([]));
}

