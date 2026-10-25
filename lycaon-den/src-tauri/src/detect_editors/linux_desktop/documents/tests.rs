use super::*;
use crate::test_support::TempDir;
use std::os::unix::fs::PermissionsExt;

/// A `flatpak` stand-in that records each argv and answers `document-export`
/// from its `mode` file: `print` writes `stdout`, `refuse` fails, `hang` waits.
struct Flatpak {
    dir: TempDir,
}

impl Flatpak {
    fn new(mode: &str, stdout: &[u8]) -> Self {
        let dir = TempDir::new("flatpak-documents");
        let program = dir.join("flatpak");
        std::fs::write(
            &program,
            "#!/bin/sh\n\
             dir=$(dirname \"$0\")\n\
             printf '%s\\037' \"$@\" >> \"$dir/calls\"\n\
             printf '\\n' >> \"$dir/calls\"\n\
             [ \"$1\" = document-unexport ] && exit 0\n\
             case $(cat \"$dir/mode\") in\n\
             print) cat \"$dir/stdout\";;\n\
             refuse) echo 'app id is not allowed' >&2; exit 1;;\n\
             hang) exec sleep 30;;\n\
             esac\n",
        )
        .unwrap();
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700)).unwrap();
        std::fs::write(dir.join("mode"), mode).unwrap();
        std::fs::write(dir.join("stdout"), stdout).unwrap();
        Self { dir }
    }

    fn program(&self) -> PathBuf {
        self.dir.join("flatpak")
    }

    fn calls(&self) -> Vec<Vec<String>> {
        std::fs::read_to_string(self.dir.join("calls"))
            .unwrap_or_default()
            .lines()
            .map(|line| line.split('\u{1f}').filter(|word| !word.is_empty()).map(String::from).collect())
            .collect()
    }
}

const APP: &str = "com.visualstudio.code";

#[test]
fn the_launcher_exports_the_exact_original_path_for_the_selected_app() {
    let original = Path::new("/outside/project/notes é.txt");
    let flatpak = Flatpak::new("print", "/run/user/1000/doc/Ab_c-1/notes é.txt\n".as_bytes());
    let export = export(&flatpak.program(), original, APP).unwrap();
    assert_eq!(export.path, Path::new("/run/user/1000/doc/Ab_c-1/notes é.txt"));
    assert_eq!(
        flatpak.calls(),
        vec![vec![
            "document-export", "--unique", "--transient", "--allow-read", "--allow-write",
            "--app=com.visualstudio.code", "--", "/outside/project/notes é.txt",
        ]]
    );
    export.retain();
    assert_eq!(flatpak.calls().len(), 1, "a retained grant is not removed");
}

#[test]
fn an_unused_grant_is_removed_by_its_document_id() {
    let flatpak = Flatpak::new("print", b"/run/user/1000/doc/QoJN_PMvva1bFhLA1_AVcQ/project\n");
    drop(export(&flatpak.program(), Path::new("/outside/project"), APP).unwrap());
    assert_eq!(flatpak.calls()[1], vec!["document-unexport", "--doc-id", "QoJN_PMvva1bFhLA1_AVcQ"]);
}

#[test]
fn refusal_and_silence_are_errors_not_the_original_path() {
    let refused = Flatpak::new("refuse", b"");
    let error = export(&refused.program(), Path::new("/outside/a.txt"), APP).err().unwrap();
    assert!(error.contains("app id is not allowed"), "{error}");

    let silent = Flatpak::new("hang", b"");
    let started = Instant::now();
    let error = export_within(&silent.program(), Path::new("/outside/a.txt"), APP, Duration::from_millis(300)).err().unwrap();
    assert!(error.contains("did not answer"), "{error}");
    assert!(started.elapsed() < Duration::from_secs(10));

    let missing = Path::new("/nonexistent/paintedwolf/flatpak");
    assert!(export(missing, Path::new("/outside/a.txt"), APP).is_err());
    assert!(export(&refused.program(), Path::new("relative.txt"), APP).is_err());
}

#[test]
fn only_an_export_of_the_same_name_under_a_document_id_is_accepted() {
    let original = Path::new("/outside/a é.txt");
    for stdout in [
        &b""[..],
        b"relative/doc/abc/a \xc3\xa9.txt\n",
        b"/run/user/1000/doc/abc/other.txt\n",
        b"/run/user/1000/doc/../a \xc3\xa9.txt\n",
        b"/run/user/1000/doc/a b/a \xc3\xa9.txt\n",
        b"/abc/a \xc3\xa9.txt\n",
        b"/run/user/1000/doc/abc/a \xc3\xa9.txt\n/second\n",
    ] {
        assert!(exported_path(stdout.to_vec(), original).is_err(), "{:?}", String::from_utf8_lossy(stdout));
    }
    assert_eq!(
        exported_path(b"/run/user/1000/doc/abc/a \xc3\xa9.txt".to_vec(), original).unwrap(),
        (PathBuf::from("/run/user/1000/doc/abc/a é.txt"), "abc".to_string())
    );
}
