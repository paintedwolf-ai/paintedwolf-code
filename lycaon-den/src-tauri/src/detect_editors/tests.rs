use super::catalog::INTELLIJ_IDEA_ENTRY;
use super::*;
use std::collections::HashSet;

/// Probe that reports true for an exact set of paths and a set of CLIs.
struct FakeProbe {
    paths: HashSet<String>,
    clis: HashSet<String>,
}

impl FakeProbe {
    fn new(paths: &[&str], clis: &[&str]) -> Self {
        Self {
            paths: paths.iter().map(|s| s.to_string()).collect(),
            clis: clis.iter().map(|s| s.to_string()).collect(),
        }
    }
}

impl EditorProbe for FakeProbe {
    fn path_exists(&self, path: &str) -> bool {
        self.paths.contains(path)
    }
    fn cli_on_path(&self, cli: &str) -> bool {
        self.clis.contains(cli)
    }
}

#[test]
fn find_among_prefers_cli_on_path() {
    let probe = FakeProbe::new(&["/Applications/Cursor.app"], &["cursor"]);
    let got = find_among(&probe, "cursor", &["/Applications/Cursor.app".to_string()]);
    assert_eq!(got.as_deref(), Some("cli:cursor"));
}

#[test]
fn find_among_falls_back_to_first_existing_candidate() {
    let probe = FakeProbe::new(&["/snap/bin/code"], &[]);
    let got = find_among(
        &probe,
        "code",
        &["/usr/bin/code".to_string(), "/snap/bin/code".to_string()],
    );
    assert_eq!(got.as_deref(), Some("/snap/bin/code"));
}

#[test]
fn find_among_skips_cli_probe_when_cli_empty() {
    // An empty CLI skips the PATH probe.
    let probe = FakeProbe::new(&["/Applications/Nova.app"], &["nova"]);
    let got = find_among(&probe, "", &["/Applications/Nova.app".to_string()]);
    assert_eq!(got.as_deref(), Some("/Applications/Nova.app"));
}

#[test]
fn find_among_returns_none_when_absent() {
    let probe = FakeProbe::new(&[], &[]);
    let got = find_among(&probe, "code", &["/usr/bin/code".to_string()]);
    assert!(got.is_none());
}

#[test]
fn detect_reports_cursor_when_app_bundle_present() {
    // A bundle alone is sufficient.
    #[cfg(target_os = "macos")]
    {
        let probe = FakeProbe::new(&["/Applications/Cursor.app"], &[]);
        let got = detect_installed_editors_with(&probe);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].id, "cursor");
        assert_eq!(got[0].install_path, "/Applications/Cursor.app");
    }
}

#[test]
fn detect_preserves_catalog_order() {
    // Catalog order determines the first result.
    #[cfg(target_os = "macos")]
    {
        let probe = FakeProbe::new(
            &[
                "/Applications/Cursor.app",
                "/Applications/Visual Studio Code.app",
            ],
            &[],
        );
        let got = detect_installed_editors_with(&probe);
        assert_eq!(
            got.iter().map(|e| e.id.clone()).collect::<Vec<_>>(),
            ["cursor", "vscode"]
        );
    }
}

#[test]
fn detect_omits_absent_editors() {
    #[cfg(target_os = "macos")]
    {
        let probe = FakeProbe::new(&["/Applications/Visual Studio Code.app"], &[]);
        let got = detect_installed_editors_with(&probe);
        assert_eq!(
            got.iter().map(|e| e.id.clone()).collect::<Vec<_>>(),
            ["vscode"]
        );
    }
}

#[test]
fn detect_reports_bundle_only_editor_when_cli_absent() {
    // Bundle-only entries remain detectable.
    #[cfg(target_os = "macos")]
    {
        let probe = FakeProbe::new(&["/Applications/Nova.app"], &[]);
        let got = detect_installed_editors_with(&probe);
        let nova = got.iter().find(|e| e.id == "nova").expect("nova detected");
        assert_eq!(nova.install_path, "/Applications/Nova.app");
    }
}

#[test]
fn detect_prefers_intellij_ultimate_over_community() {
    // Catalog priority selects the first installed edition.
    #[cfg(target_os = "macos")]
    {
        let probe = FakeProbe::new(
            &[
                "/Applications/IntelliJ IDEA.app",
                "/Applications/IntelliJ IDEA CE.app",
            ],
            &[],
        );
        let got = detect_installed_editors_with(&probe);
        let idea = got
            .iter()
            .find(|e| e.id == "intellij-idea")
            .expect("idea detected");
        assert_eq!(idea.install_path, "/Applications/IntelliJ IDEA.app");
    }
}

#[test]
fn detect_reports_intellij_community_when_ultimate_absent() {
    #[cfg(target_os = "macos")]
    {
        let probe = FakeProbe::new(&["/Applications/IntelliJ IDEA CE.app"], &[]);
        let got = detect_installed_editors_with(&probe);
        let idea = got
            .iter()
            .find(|e| e.id == "intellij-idea")
            .expect("idea detected");
        assert_eq!(idea.install_path, "/Applications/IntelliJ IDEA CE.app");
    }
}

#[test]
#[cfg(target_os = "macos")]
fn macos_bundle_candidates_lists_editions_then_locations() {
    // Each edition's system path precedes the next edition.
    let got = macos_bundle_candidates_with_home(&INTELLIJ_IDEA_ENTRY, Some("/Users/test"));
    assert_eq!(got.first().unwrap(), "/Applications/IntelliJ IDEA.app");
    assert_eq!(
        got.get(1).unwrap(),
        "/Users/test/Applications/IntelliJ IDEA.app"
    );
    assert_eq!(
        got.get(2).unwrap(),
        "/Applications/IntelliJ IDEA Ultimate.app"
    );
    assert_eq!(got.get(4).unwrap(), "/Applications/IntelliJ IDEA CE.app");
}

#[test]
fn catalog_ids_are_unique() {
    let mut seen = HashSet::new();
    for entry in catalog() {
        assert!(seen.insert(entry.id), "duplicate editor id: {}", entry.id);
    }
}

#[test]
fn catalog_includes_expected_macos_editors() {
    let ids: Vec<&str> = catalog().iter().map(|e| e.id).collect();
    for expected in [
        "cursor",
        "vscode",
        "vscode-insiders",
        "vscodium",
        "windsurf",
        "sublime",
        "bbedit",
        "textmate",
        "coteditor",
        "nova",
        "zed",
        "xcode",
        "intellij-idea",
        "webstorm",
        "pycharm",
        "goland",
        "rubymine",
        "phpstorm",
        "clion",
        "android-studio",
        "rider",
        "macvim",
        "neovide",
    ] {
        assert!(ids.contains(&expected), "missing editor: {expected}");
    }
}
