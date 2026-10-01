//! Rust host-wire mirrors match fixtures generated from Go host types.
//! Source discovery requires each mirror to declare its fixture binding.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::{Path, PathBuf};

use serde_json::Value;

const FIXTURES: &str = "tests/host_wire_fixtures.json";
const GO_FIXTURE_TEST: &str = "lycaon/test/contract/wire/rust_host_wire_fixtures_contract_test.go";

/// A source file is host-facing when it talks to the engine or lives in the
/// sidecar module that reads the engine's files and startup stream.
const HOST_FACING_MARKERS: &[&str] = &["api_token", "/v1/", "SidecarInfo", "SidecarState"];

enum Binding {
    /// Reads one host JSON object.
    Mirror {
        fixture: &'static str,
        /// Host keys the shell ignores, each with its reason.
        ignored: &'static [(&'static str, &'static str)],
        forward: Option<Forward>,
    },
    /// Enum whose variants are exactly a host vocabulary.
    Vocabulary { vocabulary: &'static str },
    /// Enum whose variants cover every value the host wrote for a field.
    ObservedValues { fixture: &'static str, field: &'static str },
    /// Deserialize type that never carries engine output.
    NotHostWire { reason: &'static str },
}

/// A mirror whose fields are re-serialized to the frontend by another type.
struct Forward {
    rust_type: &'static str,
    /// Host key to frontend key, where the shell renames a field.
    renames: &'static [(&'static str, &'static str)],
    /// TypeScript object type that reads the forwarded shape.
    ts_file: &'static str,
    ts_type: &'static str,
}

struct Entry {
    file: &'static str,
    rust_type: &'static str,
    binding: Binding,
}

const HOST_WIRE_MIRRORS: &[Entry] = &[
    Entry {
        file: "src/managed_secret_reveal.rs",
        rust_type: "HostError",
        binding: Binding::Mirror {
            fixture: "error_response",
            ignored: &[
                ("details", "reveal notices render from host copy, not structured context"),
                ("retryable", "a reveal is always a fresh user action"),
                ("tier", "HTTP error bodies are always non_catastrophic"),
            ],
            forward: Some(Forward {
                rust_type: "RevealCommandError",
                renames: &[],
                ts_file: "../src/platform/files/managed-secret-reveal.ts",
                ts_type: "NativeRevealFailure",
            }),
        },
    },
    Entry {
        file: "src/managed_secret_reveal.rs",
        rust_type: "RevealChallenge",
        binding: Binding::Mirror {
            fixture: "managed_secret_reveal_challenge",
            ignored: &[
                ("version", "the completion request is bound by challenge_id"),
                ("expires_at", "the host enforces expiry when the challenge completes"),
            ],
            forward: None,
        },
    },
    Entry {
        file: "src/managed_secret_reveal.rs",
        rust_type: "ManagedSecretReveal",
        binding: Binding::Mirror {
            fixture: "managed_secret_reveal_response",
            ignored: &[],
            forward: None,
        },
    },
    Entry {
        file: "src/external_attachment_import.rs",
        rust_type: "ImportedAttachmentReceipt",
        binding: Binding::Mirror {
            fixture: "attachment_upload_response",
            ignored: &[],
            forward: None,
        },
    },
    Entry {
        file: "src/external_attachment_import.rs",
        rust_type: "ImportedVideoFacts",
        binding: Binding::Mirror {
            fixture: "attachment_video_facts",
            ignored: &[],
            forward: None,
        },
    },
    Entry {
        file: "src/external_attachment_import.rs",
        rust_type: "SidecarErrorBody",
        binding: Binding::Mirror {
            fixture: "error_response",
            ignored: &[
                ("title", "import failures map to the shell's closed attachment codes"),
                ("details", "import failures map to the shell's closed attachment codes"),
                ("retryable", "import failures map to the shell's closed attachment codes"),
                ("suggested_action", "import failures map to the shell's closed attachment codes"),
                ("actions", "import failures map to the shell's closed attachment codes"),
                ("tier", "import failures map to the shell's closed attachment codes"),
                ("scope", "import failures map to the shell's closed attachment codes"),
                ("resolution", "import failures map to the shell's closed attachment codes"),
            ],
            forward: None,
        },
    },
    Entry {
        file: "src/sidecar.rs",
        rust_type: "SidecarInfo",
        binding: Binding::NotHostWire {
            reason: "the shell builds it from daemon.json and api.token for the frontend",
        },
    },
    Entry {
        file: "src/sidecar/daemon.rs",
        rust_type: "DaemonManifest",
        binding: Binding::Mirror {
            fixture: "daemon_manifest",
            ignored: &[("started_at", "the shell checks the live pid, not the manifest age")],
            forward: None,
        },
    },
    Entry {
        file: "src/sidecar/startup.rs",
        rust_type: "StartupRecord",
        binding: Binding::Mirror {
            fixture: "startup_record",
            ignored: &[],
            forward: None,
        },
    },
    Entry {
        file: "src/sidecar/startup.rs",
        rust_type: "StartupPhase",
        binding: Binding::Vocabulary {
            vocabulary: "startup_phase",
        },
    },
    Entry {
        file: "src/sidecar/startup.rs",
        rust_type: "StartupKind",
        binding: Binding::ObservedValues {
            fixture: "startup_record",
            field: "kind",
        },
    },
];

#[derive(Debug, Clone, PartialEq)]
enum Kind {
    Struct,
    Enum,
}

#[derive(Debug, Clone)]
struct Field {
    rust_name: String,
    ty: String,
    attrs: Vec<String>,
}

#[derive(Debug, Clone)]
struct Item {
    name: String,
    kind: Kind,
    attrs: Vec<String>,
    members: Vec<Field>,
}

impl Item {
    fn derives(&self, name: &str) -> bool {
        self.attrs
            .iter()
            .any(|attr| attr.starts_with("#[derive(") && attr.contains(name))
    }

    fn serde_attr(&self, key: &str) -> Option<String> {
        serde_value(&self.attrs, key)
    }

    fn wire_name(&self, member: &Field) -> String {
        if let Some(rename) = serde_value(&member.attrs, "rename") {
            return rename;
        }
        match self.serde_attr("rename_all").as_deref() {
            None => member.rust_name.clone(),
            Some("snake_case") => snake_case(&member.rust_name),
            Some("lowercase") => member.rust_name.to_lowercase(),
            Some(other) => panic!(
                "{}: rename_all = {other:?} is not modeled by host_wire_mirrors.rs; teach wire_name it",
                self.name
            ),
        }
    }

    /// Keys serde reads (`de`) or writes (`!de`).
    fn wire_names(&self, de: bool) -> BTreeMap<String, Field> {
        let mut names = BTreeMap::new();
        for member in &self.members {
            if has_serde_flag(&member.attrs, "flatten") {
                panic!(
                    "{}.{}: #[serde(flatten)] is not modeled by host_wire_mirrors.rs; mirror the host keys explicitly",
                    self.name, member.rust_name
                );
            }
            let skip = if de { "skip_deserializing" } else { "skip_serializing" };
            if has_serde_flag(&member.attrs, "skip") || has_serde_flag(&member.attrs, skip) {
                continue;
            }
            names.insert(self.wire_name(member), member.clone());
        }
        names
    }
}

fn serde_value(attrs: &[String], key: &str) -> Option<String> {
    let needle = format!("{key} = \"");
    attrs
        .iter()
        .filter(|attr| attr.starts_with("#[serde("))
        .find_map(|attr| {
            let start = attr.find(&needle)? + needle.len();
            let end = attr[start..].find('"')? + start;
            Some(attr[start..end].to_string())
        })
}

fn has_serde_flag(attrs: &[String], flag: &str) -> bool {
    attrs.iter().filter(|attr| attr.starts_with("#[serde(")).any(|attr| {
        attr["#[serde(".len()..attr.len() - 2]
            .split(',')
            .any(|part| part.trim() == flag)
    })
}

fn snake_case(value: &str) -> String {
    let mut out = String::new();
    for (i, ch) in value.chars().enumerate() {
        if ch.is_uppercase() {
            if i > 0 {
                out.push('_');
            }
            out.extend(ch.to_lowercase());
        } else {
            out.push(ch);
        }
    }
    out
}

fn manifest_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
}

fn rust_sources(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<_> = fs::read_dir(dir)
        .unwrap_or_else(|e| panic!("read {}: {e}", dir.display()))
        .map(|entry| entry.expect("dir entry").path())
        .collect();
    entries.sort();
    for path in entries {
        if path.is_dir() {
            rust_sources(&path, out);
        } else if path.extension().is_some_and(|ext| ext == "rs") {
            out.push(path);
        }
    }
}

/// Production source: unit-test modules and test-only files are not mirrors.
fn production_source(path: &Path) -> Option<String> {
    if path.file_name().is_some_and(|name| name == "tests.rs") {
        return None;
    }
    let raw = fs::read_to_string(path).unwrap_or_else(|e| panic!("read {}: {e}", path.display()));
    Some(match raw.find("#[cfg(test)]\nmod tests {") {
        Some(end) => raw[..end].to_string(),
        None => raw,
    })
}

fn is_host_facing(relative: &str, source: &str) -> bool {
    relative.starts_with("src/sidecar/")
        || HOST_FACING_MARKERS.iter().any(|marker| source.contains(marker))
}

fn item_header(line: &str) -> Option<(Kind, String)> {
    let mut words = line.split_whitespace().peekable();
    while let Some(word) = words.next() {
        let kind = match word {
            "struct" => Kind::Struct,
            "enum" => Kind::Enum,
            w if w == "pub" || w.starts_with("pub(") => continue,
            _ => return None,
        };
        let name: String = words
            .next()?
            .chars()
            .take_while(|c| c.is_alphanumeric() || *c == '_')
            .collect();
        return Some((kind, name));
    }
    None
}

fn parse_items(source: &str) -> Vec<Item> {
    let lines: Vec<&str> = source.lines().collect();
    let mut items = Vec::new();
    let mut attrs: Vec<String> = Vec::new();
    let mut i = 0;
    while i < lines.len() {
        let line = lines[i].trim();
        i += 1;
        if line.starts_with("#[") {
            attrs.push(collect_attr(line, &lines, &mut i));
            continue;
        }
        if line.starts_with("//") || line.is_empty() {
            continue;
        }
        let Some((kind, name)) = item_header(line) else {
            attrs.clear();
            continue;
        };
        let mut members = Vec::new();
        if line.ends_with('{') {
            members = parse_body(&kind, &lines, &mut i);
        }
        items.push(Item {
            name,
            kind,
            attrs: std::mem::take(&mut attrs),
            members,
        });
    }
    items
}

fn collect_attr(first: &str, lines: &[&str], i: &mut usize) -> String {
    let mut attr = first.to_string();
    while attr.matches('[').count() > attr.matches(']').count() && *i < lines.len() {
        attr.push(' ');
        attr.push_str(lines[*i].trim());
        *i += 1;
    }
    attr
}

fn parse_body(kind: &Kind, lines: &[&str], i: &mut usize) -> Vec<Field> {
    let mut members = Vec::new();
    let mut attrs = Vec::new();
    let mut depth = 1usize;
    while *i < lines.len() && depth > 0 {
        let line = lines[*i].trim();
        *i += 1;
        let at_top = depth == 1;
        depth += line.matches('{').count();
        depth = depth.saturating_sub(line.matches('}').count());
        if !at_top || line.starts_with("//") || line.is_empty() || line == "}" {
            continue;
        }
        if line.starts_with("#[") {
            attrs.push(collect_attr(line, lines, i));
            continue;
        }
        let declaration = line
            .trim_start_matches("pub(crate) ")
            .trim_start_matches("pub(super) ")
            .trim_start_matches("pub ");
        let (name, ty) = match kind {
            Kind::Struct => match declaration.split_once(':') {
                Some((name, ty)) => (name.trim(), ty.trim().trim_end_matches(',')),
                None => continue,
            },
            Kind::Enum => {
                let name = declaration
                    .split(|c: char| !(c.is_alphanumeric() || c == '_'))
                    .next()
                    .unwrap_or_default();
                (name, "")
            }
        };
        members.push(Field {
            rust_name: name.to_string(),
            ty: ty.to_string(),
            attrs: std::mem::take(&mut attrs),
        });
    }
    members
}

struct Source {
    items: Vec<Item>,
}

impl Source {
    fn item(&self, name: &str) -> Option<&Item> {
        self.items.iter().find(|item| item.name == name)
    }
}

fn load_fixtures() -> Value {
    let path = manifest_dir().join(FIXTURES);
    let raw = fs::read_to_string(&path).unwrap_or_else(|e| {
        panic!(
            "read {}: {e}\nFix: generate it from the Go host types with \
             UPDATE_RUST_HOST_WIRE_FIXTURES=1 ./task test:contract -- ./test/contract/wire/...",
            path.display()
        )
    });
    serde_json::from_str(&raw).unwrap_or_else(|e| panic!("parse {FIXTURES}: {e}"))
}

fn fixture_examples<'a>(fixtures: &'a Value, fixture: &str) -> (&'a str, Vec<&'a Value>) {
    let entry = &fixtures["fixtures"][fixture];
    let source = entry["source"].as_str().unwrap_or_else(|| {
        panic!("{FIXTURES} has no fixture {fixture:?}; add its Go type to {GO_FIXTURE_TEST} and refresh")
    });
    let examples = entry["examples"].as_array().expect("fixture examples");
    (source, examples.iter().collect())
}

fn object_keys(examples: &[&Value]) -> BTreeSet<String> {
    examples
        .iter()
        .filter_map(|example| example.as_object())
        .flat_map(|object| object.keys().cloned())
        .collect()
}

/// Closest host key, so a misnamed field names its intended key.
fn nearest<'a>(name: &str, keys: &'a BTreeSet<String>) -> Option<&'a String> {
    keys.iter()
        .filter(|key| key.starts_with(name) || name.starts_with(key.as_str()))
        .min_by_key(|key| key.len().abs_diff(name.len()))
}

fn inner_type(ty: &str) -> &str {
    let mut ty = ty.trim();
    for wrapper in ["Option<", "Vec<", "Box<"] {
        if let Some(rest) = ty.strip_prefix(wrapper) {
            ty = rest.strip_suffix('>').unwrap_or(rest);
            return inner_type(ty);
        }
    }
    ty
}

struct MirrorCheck<'a> {
    file: &'a str,
    source: &'a Source,
    go_type: &'a str,
    violations: &'a mut Vec<String>,
}

impl MirrorCheck<'_> {
    /// Compares one Rust struct with the host examples, recursing into nested
    /// structs the same file declares.
    fn compare(&mut self, item: &Item, path: &str, examples: &[&Value], ignored: &[(&str, &str)]) {
        let host = object_keys(examples);
        let rust = item.wire_names(true);
        let ignored_here: BTreeMap<&str, &str> = ignored
            .iter()
            .filter_map(|(key, reason)| {
                let local = key.strip_prefix(path)?;
                (!local.contains('.')).then_some((local, *reason))
            })
            .collect();
        for (name, field) in &rust {
            if !host.contains(name) {
                let hint = nearest(name, &host)
                    .map(|key| format!(" Did you mean `{key}`?"))
                    .unwrap_or_default();
                self.violations.push(format!(
                    "{file}: {rust_type} reads `{path}{name}`, but the host type {go} never sends that key \
                     (host keys: {host:?}).{hint} serde leaves the field empty, so the host's value is silently lost.\n    \
                     Fix: name the Rust field after the host's JSON key (or add #[serde(rename = \"<host key>\")]).",
                    file = self.file,
                    rust_type = item.name,
                    go = self.go_type,
                ));
                continue;
            }
            let nested_ty = inner_type(&field.ty);
            if let Some(nested) = self.source.item(nested_ty).filter(|i| i.kind == Kind::Struct) {
                let nested_examples: Vec<&Value> = examples
                    .iter()
                    .filter_map(|example| example.get(name))
                    .flat_map(|value| match value {
                        Value::Array(values) => values.iter().collect(),
                        other => vec![other],
                    })
                    .collect();
                let nested = nested.clone();
                self.compare(&nested, &format!("{path}{name}."), &nested_examples, ignored);
            }
        }
        for key in &host {
            if rust.contains_key(key) {
                if ignored_here.contains_key(key.as_str()) {
                    self.violations.push(format!(
                        "{}: {} lists `{path}{key}` as ignored but reads it.\n    \
                         Fix: remove it from `ignored` in tests/host_wire_mirrors.rs.",
                        self.file, item.name
                    ));
                }
                continue;
            }
            if !ignored_here.contains_key(key.as_str()) {
                self.violations.push(format!(
                    "{}: the host type {} sends `{path}{key}`, which {} neither reads nor lists as ignored, \
                     so the shell drops it before the frontend sees it.\n    \
                     Fix: add the field to {} (and forward it), or list it under `ignored` in \
                     tests/host_wire_mirrors.rs with the reason the shell may drop it.",
                    self.file, self.go_type, item.name, item.name
                ));
            }
        }
        for key in ignored_here.keys() {
            if !host.contains(*key) {
                self.violations.push(format!(
                    "{}: {} ignores `{path}{key}`, which the host type {} no longer sends.\n    \
                     Fix: remove the stale entry from `ignored` in tests/host_wire_mirrors.rs.",
                    self.file, item.name, self.go_type
                ));
            }
        }
    }
}

fn check_forward(file: &str, source: &Source, mirror: &Item, forward: &Forward, violations: &mut Vec<String>) {
    let Some(target) = source.item(forward.rust_type) else {
        violations.push(format!(
            "{file}: forward target {} not found.\n    Fix: update the entry for {} in tests/host_wire_mirrors.rs.",
            forward.rust_type, mirror.name
        ));
        return;
    };
    let sent = target.wire_names(false);
    for host_key in mirror.wire_names(true).keys() {
        let frontend_key = forward
            .renames
            .iter()
            .find(|(from, _)| from == host_key)
            .map_or(host_key.as_str(), |(_, to)| *to);
        if !sent.contains_key(frontend_key) {
            violations.push(format!(
                "{file}: {} reads host key `{host_key}`, but {} never serializes `{frontend_key}` to the frontend.\n    \
                 Fix: carry the field through {} under the same key.",
                mirror.name, forward.rust_type, forward.rust_type
            ));
        }
    }
    let ts_path = manifest_dir().join(forward.ts_file);
    let ts_source = fs::read_to_string(&ts_path).unwrap_or_else(|e| panic!("read {}: {e}", ts_path.display()));
    let ts_keys = ts_object_keys(&ts_source, forward.ts_type).unwrap_or_else(|| {
        panic!(
            "type {} not found in {}; update the entry for {} in tests/host_wire_mirrors.rs",
            forward.ts_type, forward.ts_file, mirror.name
        )
    });
    let sent_keys: BTreeSet<String> = sent.keys().cloned().collect();
    for key in sent_keys.difference(&ts_keys) {
        violations.push(format!(
            "{file}: {} sends `{key}` to the frontend, but {} in {} never reads it.\n    \
             Fix: use the frontend's key in {} (or read the key in {}).",
            forward.rust_type, forward.ts_type, forward.ts_file, forward.rust_type, forward.ts_type
        ));
    }
    for key in ts_keys.difference(&sent_keys) {
        violations.push(format!(
            "{file}: {} in {} reads `{key}`, but {} never sends it, so the frontend always sees it missing.\n    \
             Fix: add `{key}` to {} and carry it from the host mirror {}.",
            forward.ts_type, forward.ts_file, forward.rust_type, forward.rust_type, mirror.name
        ));
    }
}

fn ts_object_keys(source: &str, ts_type: &str) -> Option<BTreeSet<String>> {
    let start = source.find(&format!("type {ts_type} = {{"))?;
    let body = &source[start..];
    let body = &body[body.find('{')? + 1..body.find("};")?];
    Some(
        body.lines()
            .filter_map(|line| line.trim().split_once(':'))
            .map(|(key, _)| key.trim().trim_end_matches('?').to_string())
            .filter(|key| !key.is_empty() && !key.starts_with("//"))
            .collect(),
    )
}

fn enum_values(item: &Item) -> BTreeSet<String> {
    item.wire_names(true).into_keys().collect()
}

#[test]
fn host_wire_mirrors_match_the_go_host_types() {
    let root = manifest_dir();
    let fixtures = load_fixtures();
    let mut paths = Vec::new();
    rust_sources(&root.join("src"), &mut paths);

    let mut sources: BTreeMap<String, Source> = BTreeMap::new();
    let mut violations = Vec::new();
    for path in paths {
        let relative = path.strip_prefix(&root).unwrap().to_string_lossy().replace('\\', "/");
        let Some(text) = production_source(&path) else { continue };
        if !is_host_facing(&relative, &text) {
            continue;
        }
        let source = Source { items: parse_items(&text) };
        for item in source.items.iter().filter(|item| item.derives("Deserialize")) {
            let registered = HOST_WIRE_MIRRORS
                .iter()
                .any(|entry| entry.file == relative && entry.rust_type == item.name);
            if !registered {
                violations.push(format!(
                    "{relative}: {} derives Deserialize in a host-facing file but has no entry in \
                     HOST_WIRE_MIRRORS (tests/host_wire_mirrors.rs). Every type the shell reads from the \
                     engine must be checked against the Go type the host writes.\n    \
                     Fix: add the Go type to {GO_FIXTURE_TEST}, refresh with \
                     UPDATE_RUST_HOST_WIRE_FIXTURES=1 ./task test:contract -- ./test/contract/wire/..., and bind \
                     {} to that fixture; or register it as NotHostWire with the reason.",
                    item.name, item.name
                ));
            }
        }
        sources.insert(relative, source);
    }

    for entry in HOST_WIRE_MIRRORS {
        let Some(item) = sources.get(entry.file).and_then(|source| source.item(entry.rust_type)) else {
            violations.push(format!(
                "{}: {} is registered in HOST_WIRE_MIRRORS but no longer found in a host-facing file.\n    \
                 Fix: update or remove its entry in tests/host_wire_mirrors.rs.",
                entry.file, entry.rust_type
            ));
            continue;
        };
        let source = &sources[entry.file];
        match &entry.binding {
            Binding::Mirror { fixture, ignored, forward } => {
                let (go_type, examples) = fixture_examples(&fixtures, fixture);
                let mut check = MirrorCheck {
                    file: entry.file,
                    source,
                    go_type,
                    violations: &mut violations,
                };
                check.compare(item, "", &examples, ignored);
                if let Some(forward) = forward {
                    check_forward(entry.file, source, item, forward, &mut violations);
                }
            }
            Binding::Vocabulary { vocabulary } => {
                let host: BTreeSet<String> = fixtures["vocabularies"][vocabulary]
                    .as_array()
                    .unwrap_or_else(|| panic!("{FIXTURES} has no vocabulary {vocabulary:?}"))
                    .iter()
                    .map(|value| value.as_str().unwrap().to_string())
                    .collect();
                let rust = enum_values(item);
                if rust != host {
                    violations.push(format!(
                        "{}: {} variants must equal the host vocabulary {vocabulary}.\n    \
                         missing in Rust: {:?}; unknown to the host: {:?}\n    \
                         Fix: make the enum's serde names match the Go vocabulary.",
                        entry.file,
                        entry.rust_type,
                        host.difference(&rust).collect::<Vec<_>>(),
                        rust.difference(&host).collect::<Vec<_>>()
                    ));
                }
            }
            Binding::ObservedValues { fixture, field } => {
                let (go_type, examples) = fixture_examples(&fixtures, fixture);
                let rust = enum_values(item);
                for value in examples.iter().filter_map(|example| example[field].as_str()) {
                    if !rust.contains(value) {
                        violations.push(format!(
                            "{}: {} has no variant for `{value}`, which {go_type} writes in `{field}`.\n    \
                             Fix: add the variant so the shell can read the record.",
                            entry.file, entry.rust_type
                        ));
                    }
                }
            }
            Binding::NotHostWire { reason } => {
                assert!(!reason.is_empty(), "{}: NotHostWire needs a reason", entry.rust_type);
            }
        }
    }

    violations.sort();
    violations.dedup();
    assert!(
        violations.is_empty(),
        "Rule: every Rust type in lycaon-den/src-tauri that reads engine output must match the Go host type, \
         field for field, and pass every field the frontend reads through unchanged. A misnamed field \
         deserializes as empty and drops host data without an error.\n\n{} violation(s):\n  - {}",
        violations.len(),
        violations.join("\n  - ")
    );
}

#[test]
fn source_parser_reads_serde_names() {
    let items = parse_items(
        "#[derive(Debug, Deserialize)]\n#[serde(rename_all = \"snake_case\")]\npub(super) struct Probe {\n    \
         /// Doc.\n    #[serde(rename = \"error\")]\n    message: String,\n    #[serde(default)]\n    \
         actions: Vec<String>,\n    #[serde(skip)]\n    local: u8,\n}\n\n#[derive(Deserialize)]\nenum Phase {\n    \
         UpgradeSnapshot,\n    Ready,\n}\n",
    );
    let probe = &items[0];
    assert!(probe.derives("Deserialize"));
    assert_eq!(
        probe.wire_names(true).into_keys().collect::<Vec<_>>(),
        vec!["actions".to_string(), "error".to_string()]
    );
    assert_eq!(
        enum_values(&Item {
            attrs: vec!["#[serde(rename_all = \"snake_case\")]".into()],
            ..items[1].clone()
        }),
        BTreeSet::from(["upgrade_snapshot".to_string(), "ready".to_string()])
    );
}
