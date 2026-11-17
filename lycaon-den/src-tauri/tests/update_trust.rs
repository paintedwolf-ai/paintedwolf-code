//! Tampered, replayed and foreign-key update inputs are refused end to end. A disposable key
//! generation signs a local origin's pointers and artifacts at test time; production builds
//! construct the same `Trust` from the compiled-in registry.
use base64::{engine::general_purpose::STANDARD, Engine as _};
use painted_wolf_code_lib::update_service::{
    download::{self, Progress},
    feed::{self, FeedFailure},
    staging, Candidate, Installation, Trust, UpdateChannel, UpdateError, UpdateErrorCode as Failure,
};
use std::{
    collections::HashMap,
    fs,
    io::{BufRead, BufReader, Write},
    net::TcpListener,
    path::{Path, PathBuf},
    sync::{Arc, Mutex, MutexGuard, OnceLock},
};

const RUNNING: &str = "1.0.0";
const OFFERED: &str = "1.1.0";

struct Key(minisign::KeyPair);
impl Key {
    fn new() -> Self {
        Self(minisign::KeyPair::generate_unencrypted_keypair().unwrap())
    }
    /// The registry's encoding: base64 of the minisign public key document.
    fn public(&self) -> String {
        STANDARD.encode(self.0.pk.to_box().unwrap().into_string())
    }
    /// Signatures are prehashed, the only mode the updater verifies.
    fn sign(&self, data: &[u8], comment: &str) -> String {
        let signature = minisign::sign(Some(&self.0.pk), &self.0.sk, data, Some(comment), None).unwrap();
        STANDARD.encode(signature.into_string())
    }
}

/// One key generation: its artifact key and its feed key.
struct Generation {
    number: u64,
    artifact: Key,
    feed: Key,
}
impl Generation {
    fn new(number: u64) -> Self {
        Self { number, artifact: Key::new(), feed: Key::new() }
    }
    fn sign_artifact(&self, bytes: &[u8], version: &str) -> String {
        self.artifact.sign(bytes, &format!("timestamp:1\tfile:app.tar.gz\tversion:{version}"))
    }
    /// A stable pointer signature bound to the pointer name of generation `name`.
    fn sign_pointer_named(&self, manifest: &[u8], name: u64, timestamp: u64, version: &str) -> String {
        let file = format!("latest-stable-key-{name}.json");
        self.feed.sign(manifest, &format!("timestamp:{timestamp}\tfile:{file}\tversion:{version}"))
    }
    fn sign_pointer(&self, manifest: &[u8], timestamp: u64, version: &str) -> String {
        self.sign_pointer_named(manifest, self.number, timestamp, version)
    }
}

/// A plain HTTP origin whose routes the test replaces between cases.
struct Origin {
    url: String,
    routes: Arc<Mutex<HashMap<String, Vec<u8>>>>,
}
impl Origin {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let routes: Arc<Mutex<HashMap<String, Vec<u8>>>> = Arc::default();
        let served = routes.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let mut stream = stream.unwrap();
                let mut request = String::new();
                let mut reader = BufReader::new(stream.try_clone().unwrap());
                reader.read_line(&mut request).unwrap();
                loop {
                    let mut header = String::new();
                    if reader.read_line(&mut header).unwrap() == 0 || header == "\r\n" {
                        break;
                    }
                }
                let path = request.split(' ').nth(1).unwrap_or_default().to_owned();
                let body = served.lock().unwrap().get(&path).cloned();
                let (status, body) = match body {
                    Some(body) => ("200 OK", body),
                    None => ("404 Not Found", Vec::new()),
                };
                let head = format!("HTTP/1.1 {status}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len());
                let _ = stream.write_all(head.as_bytes()).and_then(|()| stream.write_all(&body));
            }
        });
        Self { url, routes }
    }
    fn serve(&self, path: &str, body: impl Into<Vec<u8>>) {
        self.routes.lock().unwrap().insert(path.into(), body.into());
    }
}

/// Every test shares one process-wide state root, so they run one at a time.
struct World {
    root: PathBuf,
}
fn world() -> MutexGuard<'static, World> {
    static WORLD: OnceLock<Mutex<World>> = OnceLock::new();
    let world = WORLD
        .get_or_init(|| {
            let root = std::env::temp_dir().join(format!("update-trust-{}", std::process::id()));
            fs::create_dir_all(root.join("config")).unwrap();
            std::env::set_var("LYCAON_CONFIG_DIR", root.join("config"));
            Mutex::new(World { root })
        })
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    // Each test starts with no update records, whichever ran before it.
    let _ = fs::remove_dir_all(world.root.join("config/updates"));
    world
}

struct Quiet;
impl Progress for Quiet {
    fn current(&self) -> bool {
        true
    }
    async fn phase(&self, _: Installation) -> Result<(), UpdateError> {
        Ok(())
    }
    async fn downloaded(&self, _: u64, _: Option<u64>) {}
}

fn trust(generation: &Generation, origin: &Origin) -> Trust {
    Trust {
        generation: generation.number,
        artifact_key: generation.artifact.public(),
        feed_key: generation.feed.public(),
        origin: origin.url.clone(),
    }
}

const ARTIFACT_PATH: &str = "/releases/v1.1.0/app.tar.gz";

fn manifest(version: &str, origin: &Origin, artifact_signature: &str, generation: u64) -> Vec<u8> {
    serde_json::to_vec(&serde_json::json!({
        "version": version,
        "notes": "Rehearsed release.",
        "pub_date": "2026-11-16T00:00:00Z",
        "platforms": {feed::target(): {"signature": artifact_signature, "url": format!("{}{ARTIFACT_PATH}", origin.url)}},
        "update_keys": {"signing_generation": generation, "embedded_generation": generation,
                        "signing_key_fingerprint": "a".repeat(64), "embedded_key_fingerprint": "a".repeat(64)},
    }))
    .unwrap()
}

fn running() -> semver::Version {
    semver::Version::parse(RUNNING).unwrap()
}

/// Every file under `root` with its bytes, by relative path.
fn snapshot(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    fn visit(root: &Path, directory: &Path, files: &mut Vec<(PathBuf, Vec<u8>)>) {
        let mut entries: Vec<_> = fs::read_dir(directory).unwrap().map(|entry| entry.unwrap().path()).collect();
        entries.sort();
        for path in entries {
            if path.is_dir() {
                visit(root, &path, files);
            } else {
                files.push((path.strip_prefix(root).unwrap().to_path_buf(), fs::read(&path).unwrap()));
            }
        }
    }
    let mut files = Vec::new();
    if root.is_dir() {
        visit(root, root, &mut files);
    }
    files
}

fn rebind(candidate: &Candidate, signature: String) -> Candidate {
    let mut rebound = Candidate { artifact_signature: signature, ..candidate.clone() };
    rebound.release_id = rebound.identity();
    rebound
}

/// The records of the installation this test process acts as.
fn records(candidate: &Candidate) -> PathBuf {
    staging::root(candidate).unwrap().parent().unwrap().parent().unwrap().to_path_buf()
}

fn assert_nothing_staged(candidate: &Candidate, target: &Path) {
    let root = staging::root(candidate).unwrap();
    assert!(!root.join("artifact").exists(), "a refused artifact stays cached");
    assert!(snapshot(&root).is_empty(), "a refused download leaves bytes in staging");
    assert!(staging::read_ready().unwrap().is_none(), "a refused artifact was published");
    assert!(!records(candidate).join("transaction.json").exists(), "activation began for a refused artifact");
    let beside = fs::read_dir(target.parent().unwrap()).unwrap().count();
    assert_eq!(beside, usize::from(target.exists()), "a refused artifact prepared a bundle");
}

#[test]
fn tampered_replayed_and_foreign_inputs_are_refused_before_anything_is_staged() {
    let world = world();
    let origin = Origin::start();
    let (trusted, other) = (Generation::new(1), Generation::new(2));
    let trust = trust(&trusted, &origin);
    let state = world.root.join("portable-feed-state");
    fs::create_dir_all(&state).unwrap();
    let watermark = || fs::read(state.join("feed-state.json")).ok();

    let archive = b"portable release archive".to_vec();
    let artifact_signature = trusted.sign_artifact(&archive, OFFERED);
    let current = manifest(OFFERED, &origin, &artifact_signature, 1);
    let accept = |manifest: &[u8], signature: &str| {
        tauri::async_runtime::block_on(feed::accept_pointer(
            manifest, signature.as_bytes(), UpdateChannel::Stable, &trust, &state, &running(), 300,
        ))
    };

    let offer = accept(&current, &trusted.sign_pointer(&current, 200, OFFERED)).unwrap().unwrap();
    assert_eq!(offer.candidate.version, OFFERED);
    let accepted = watermark();
    assert!(accepted.is_some());

    let mut flipped = current.clone();
    let at = flipped.iter().position(|&byte| byte == b'R').unwrap();
    flipped[at] = b'r';
    let older = manifest("1.0.5", &origin, &artifact_signature, 1);
    for (case, manifest, signature) in [
        ("pointer byte flipped", flipped, trusted.sign_pointer(&current, 200, OFFERED)),
        ("older pointer replayed", older.clone(), trusted.sign_pointer(&older, 100, "1.0.5")),
        ("another generation's feed key", current.clone(), other.sign_pointer_named(&current, 1, 400, OFFERED)),
    ] {
        let refused = accept(&manifest, &signature).unwrap_err();
        assert!(matches!(refused, FeedFailure::Rejected(_)), "{case}: {refused:?}");
        assert_eq!(watermark(), accepted, "{case} moved the replay watermark");
    }

    let target = world.root.join("portable-install/Painted Wolf Code.app");
    fs::create_dir_all(target.parent().unwrap()).unwrap();
    let candidate = offer.candidate;
    let transfer = |candidate: &Candidate| {
        tauri::async_runtime::block_on(download::transfer(&Quiet, &target, candidate, &trust))
    };
    let mut corrupted = archive.clone();
    corrupted[0] ^= 1;
    for (case, served, candidate) in [
        ("artifact byte flipped", corrupted, candidate.clone()),
        ("another generation's artifact key", archive.clone(), rebind(&candidate, other.sign_artifact(&archive, OFFERED))),
        ("signature bound to another version", archive.clone(), rebind(&candidate, trusted.sign_artifact(&archive, "1.0.9"))),
    ] {
        origin.serve(ARTIFACT_PATH, served);
        let refused = transfer(&candidate).err().unwrap_or_else(|| panic!("{case} was prepared"));
        assert_eq!(refused.code, Failure::VerificationFailed, "{case}");
        assert_nothing_staged(&candidate, &target);
    }

    // The untampered artifact passes verification and stays cached for preparation,
    // whatever the platform then decides about this test's stand-in bundle.
    origin.serve(ARTIFACT_PATH, archive.clone());
    let _ = transfer(&candidate);
    assert_eq!(fs::read(staging::root(&candidate).unwrap().join("artifact")).unwrap(), archive);
    assert!(staging::read_ready().unwrap().is_none());
}

#[cfg(target_os = "macos")]
mod macos {
    use super::*;
    use std::process::Command;

    /// An ad-hoc-signed bundle that satisfies the development signing requirement.
    fn bundle(root: &Path, version: &str) -> PathBuf {
        let app = root.join("Painted Wolf Code.app");
        let contents = app.join("Contents");
        fs::create_dir_all(contents.join("MacOS")).unwrap();
        fs::create_dir_all(contents.join("Resources")).unwrap();
        fs::copy("/usr/bin/true", contents.join("MacOS/painted-wolf-code")).unwrap();
        fs::write(contents.join("Resources/release-version"), format!("{version}\n")).unwrap();
        fs::write(contents.join("Info.plist"), format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n\
             <plist version=\"1.0\"><dict><key>CFBundleExecutable</key><string>painted-wolf-code</string>\
             <key>CFBundleIdentifier</key><string>dev.paintedwolf.code</string>\
             <key>CFBundleShortVersionString</key><string>{version}</string></dict></plist>\n")).unwrap();
        let signed = Command::new("/usr/bin/codesign")
            .args(["--force", "--sign", "-", "--identifier", "dev.paintedwolf.code"])
            .arg(&app)
            .output()
            .unwrap();
        assert!(signed.status.success(), "{signed:?}");
        app
    }

    fn archive(app: &Path) -> Vec<u8> {
        let mut builder = tar::Builder::new(flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default()));
        builder.append_dir_all("Painted Wolf Code.app", app).unwrap();
        builder.into_inner().unwrap().finish().unwrap()
    }

    #[test]
    fn tampered_inputs_leave_the_installed_bundle_byte_identical() {
        let world = world();
        let origin = Origin::start();
        let (trusted, other) = (Generation::new(1), Generation::new(2));
        let trust = trust(&trusted, &origin);
        let install = world.root.join("macos-install");
        fs::create_dir_all(&install).unwrap();
        let target = bundle(&install, RUNNING);
        let installed = snapshot(&target);
        let built = world.root.join("macos-build");
        fs::create_dir_all(&built).unwrap();
        let release = archive(&bundle(&built, OFFERED));
        let artifact_signature = trusted.sign_artifact(&release, OFFERED);
        let current = manifest(OFFERED, &origin, &artifact_signature, 1);
        let pointer = "/updates/stable/key-1/latest.json";
        let fetch = || tauri::async_runtime::block_on(feed::fetch(UpdateChannel::Stable, RUNNING, &trust));

        origin.serve(pointer, current.clone());
        origin.serve(&format!("{pointer}.sig"), trusted.sign_pointer(&current, 200, OFFERED));
        let candidate = fetch().unwrap().unwrap().candidate;
        let mut flipped = current.clone();
        let at = flipped.iter().position(|&byte| byte == b'R').unwrap();
        flipped[at] = b'r';
        let older = manifest("1.0.5", &origin, &artifact_signature, 1);
        for (case, manifest, signature) in [
            ("pointer byte flipped", flipped, trusted.sign_pointer(&current, 200, OFFERED)),
            ("older pointer replayed", older.clone(), trusted.sign_pointer(&older, 100, "1.0.5")),
            ("another generation's feed key", current.clone(), other.sign_pointer_named(&current, 1, 400, OFFERED)),
        ] {
            origin.serve(pointer, manifest);
            origin.serve(&format!("{pointer}.sig"), signature);
            let refused = fetch().unwrap_err();
            assert!(matches!(refused, FeedFailure::Rejected(_)), "{case}: {refused:?}");
        }

        let transfer = |candidate: &Candidate| {
            tauri::async_runtime::block_on(download::transfer(&Quiet, &target, candidate, &trust))
        };
        let mut corrupted = release.clone();
        let middle = corrupted.len() / 2;
        corrupted[middle] ^= 1;
        for (case, served, candidate) in [
            ("artifact byte flipped", corrupted, candidate.clone()),
            ("another generation's artifact key", release.clone(), rebind(&candidate, other.sign_artifact(&release, OFFERED))),
            ("signature bound to another version", release.clone(), rebind(&candidate, trusted.sign_artifact(&release, "1.0.9"))),
        ] {
            origin.serve(ARTIFACT_PATH, served);
            let refused = transfer(&candidate).err().unwrap_or_else(|| panic!("{case} was prepared"));
            assert_eq!(refused.code, Failure::VerificationFailed, "{case}");
            assert_nothing_staged(&candidate, &target);
            assert_eq!(snapshot(&target), installed, "{case} changed the installed bundle");
        }

        // The untampered release prepares beside the installation and is published as ready;
        // the installed bundle stays as it was until activation.
        origin.serve(ARTIFACT_PATH, release);
        let identity = transfer(&candidate).unwrap();
        staging::publish(&target, &candidate, identity, None).unwrap();
        let ready = staging::read_ready().unwrap().unwrap();
        assert_eq!(ready.candidate.release_id, candidate.release_id);
        assert!(ready.prepared_bundle.join("Contents/MacOS/painted-wolf-code").is_file());
        assert_eq!(snapshot(&target), installed);
    }
}
