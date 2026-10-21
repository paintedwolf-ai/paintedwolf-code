//! Shared format-2 Linux package and Nix generation manifest reader.

use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, BTreeSet};
use std::io::Read;
use std::path::{Component, Path, PathBuf};

pub(crate) const FORMAT_VERSION: u32 = 2;
pub(crate) const PACKAGE_NAME: &str = "painted-wolf-code";
pub(crate) const MANIFEST_LIMIT: u64 = 4 << 20;

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Manifest {
    pub(crate) format_version: u32,
    pub(crate) package: String,
    pub(crate) version: String,
    pub(crate) architecture: String,
    pub(crate) install_root: PathBuf,
    pub(crate) executables: Vec<Executable>,
    pub(crate) payload: Vec<PayloadEntry>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Executable {
    pub(crate) path: PathBuf,
    pub(crate) sha256: String,
    pub(crate) size: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct PayloadEntry {
    pub(crate) path: String,
    pub(crate) kind: String,
    pub(crate) mode: u32,
    #[serde(deserialize_with = "Option::deserialize")]
    pub(crate) sha256: Option<String>,
    #[serde(deserialize_with = "Option::deserialize")]
    pub(crate) size: Option<u64>,
    #[serde(deserialize_with = "Option::deserialize")]
    pub(crate) target: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum VerifyError {
    ReadFailed,
    IdentityMismatch,
}

pub(crate) fn valid_digest(digest: &str) -> bool {
    digest.len() == 64 && digest.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn clean_absolute(path: &Path) -> bool {
    let Some(raw) = path.to_str() else { return false };
    path.is_absolute()
        && !raw.ends_with('/')
        && !raw.contains("//")
        && path.components().all(|part| matches!(part, Component::RootDir | Component::Normal(_)))
}

fn clean_relative(path: &str) -> bool {
    !path.is_empty()
        && path != "."
        && path != "manifest.json"
        && !path.starts_with('/')
        && !path.ends_with('/')
        && !path.contains("//")
        && Path::new(path).components().all(|part| matches!(part, Component::Normal(_)))
}

fn resolve_relative(base_dir: &str, target: &str) -> Option<String> {
    if target.is_empty() || target.starts_with('/') || target.contains("//") {
        return None;
    }
    let mut parts: Vec<&str> = if base_dir.is_empty() { Vec::new() } else { base_dir.split('/').collect() };
    for part in target.split('/') {
        match part {
            "" | "." => {}
            ".." => { parts.pop()?; }
            normal => parts.push(normal),
        }
    }
    Some(parts.join("/"))
}

impl Manifest {
    pub(crate) fn from_reader(reader: impl Read, max: u64, root: &Path) -> Result<Self, String> {
        let mut raw = Vec::new();
        reader.take(max + 1).read_to_end(&mut raw).map_err(|e| e.to_string())?;
        if raw.len() as u64 > max {
            return Err("manifest exceeds size limit".into());
        }
        Self::from_slice(&raw, root)
    }

    pub(crate) fn from_slice(raw: &[u8], root: &Path) -> Result<Self, String> {
        let manifest: Self = serde_json::from_slice(raw).map_err(|e| e.to_string())?;
        manifest.validate(root)?;
        Ok(manifest)
    }

    pub(crate) fn validate(&self, root: &Path) -> Result<(), String> {
        if self.format_version != FORMAT_VERSION
            || self.package != PACKAGE_NAME
            || self.version.is_empty()
            || !matches!(self.architecture.as_str(), "x86_64" | "aarch64")
            || self.install_root != root
            || !clean_absolute(&self.install_root)
            || self.executables.is_empty()
            || self.executables.len() > 8192
        {
            return Err("manifest header does not match format 2".into());
        }
        let mut seen_execs = BTreeSet::new();
        for exe in &self.executables {
            if !clean_absolute(&exe.path) || !seen_execs.insert(&exe.path) || !valid_digest(&exe.sha256) {
                return Err("manifest executable entry is invalid".into());
            }
        }
        self.validate_payload()
    }

    fn validate_payload(&self) -> Result<(), String> {
        if self.payload.is_empty() {
            return Err("manifest payload is empty".into());
        }
        let mut entries = BTreeMap::new();
        for item in &self.payload {
            if !clean_relative(&item.path)
                || entries.insert(item.path.as_str(), item).is_some()
                || item.mode > 0o777
                || (item.kind != "symlink" && item.mode & 0o022 != 0)
            {
                return Err("manifest payload path or mode is invalid".into());
            }
            let valid = match item.kind.as_str() {
                "file" => item.sha256.as_deref().is_some_and(valid_digest) && item.size.is_some() && item.target.is_none(),
                "directory" => item.mode & 0o500 == 0o500 && item.sha256.is_none() && item.size.is_none() && item.target.is_none(),
                "symlink" => {
                    item.mode == 0o777
                        && item.sha256.is_none()
                        && item.size.is_none()
                        && item.target.as_deref().is_some_and(|t| !t.is_empty() && !t.starts_with('/'))
                }
                _ => false,
            };
            if !valid {
                return Err("manifest payload record is invalid".into());
            }
        }
        for name in entries.keys() {
            let mut parent = Path::new(name).parent();
            while let Some(dir) = parent {
                let s = dir.to_str().unwrap_or("");
                if s.is_empty() {
                    break;
                }
                if entries.get(s).map(|e| e.kind.as_str()) != Some("directory") {
                    return Err("manifest payload parent directory is missing".into());
                }
                parent = dir.parent();
            }
        }
        for (&name, &item) in &entries {
            if item.kind != "symlink" {
                continue;
            }
            let (mut current, mut visited) = (name.to_string(), BTreeSet::new());
            while let Some(entry) = entries.get(current.as_str()) {
                if entry.kind != "symlink" {
                    break;
                }
                if !visited.insert(current.clone()) {
                    return Err("manifest payload symlink is cyclic".into());
                }
                let base = Path::new(&current).parent().and_then(Path::to_str).unwrap_or("");
                let next = resolve_relative(base, entry.target.as_deref().unwrap_or(""))
                    .ok_or_else(|| "manifest payload symlink escapes root".to_string())?;
                if next.is_empty() {
                    break;
                }
                if !entries.contains_key(next.as_str()) {
                    return Err("manifest payload symlink is dangling".into());
                }
                current = next;
            }
        }
        for exe in &self.executables {
            let Ok(rel) = exe.path.strip_prefix(&self.install_root) else { continue };
            let Some(rel_str) = rel.to_str() else {
                return Err("manifest executable path is not valid UTF-8".into());
            };
            let Some(item) = entries.get(rel_str) else {
                return Err("manifest payload omits an installed executable".into());
            };
            if item.kind != "file"
                || item.mode & 0o111 == 0
                || item.sha256.as_deref() != Some(exe.sha256.as_str())
                || item.size != Some(exe.size)
            {
                return Err("manifest executable differs from payload seal".into());
            }
        }
        Ok(())
    }

    pub(crate) fn executable(&self, path: &Path) -> Option<&Executable> {
        let mut matches = self.executables.iter().filter(|entry| entry.path == path);
        let first = matches.next()?;
        matches.next().is_none().then_some(first)
    }
}

impl Executable {
    pub(crate) fn verify_reader(&self, mut reader: impl Read) -> Result<(), VerifyError> {
        let (mut digest, mut size, mut buffer) = (Sha256::new(), 0u64, [0u8; 65536]);
        loop {
            let count = reader.read(&mut buffer).map_err(|_| VerifyError::ReadFailed)?;
            if count == 0 {
                break;
            }
            size = size.saturating_add(count as u64);
            if size > self.size {
                return Err(VerifyError::IdentityMismatch);
            }
            digest.update(&buffer[..count]);
        }
        if size != self.size || hex::encode(digest.finalize()) != self.sha256 {
            return Err(VerifyError::IdentityMismatch);
        }
        Ok(())
    }
}

#[cfg(test)]
pub(crate) fn fixture_json(
    root: &Path,
    architecture: &str,
    executables: &[(&Path, &[u8])],
    extra_files: &[(&str, &[u8], u32)],
) -> serde_json::Value {
    let mut exec_rows = Vec::new();
    let mut payload_rows: BTreeMap<String, serde_json::Value> = BTreeMap::new();
    let add_parents = |rel: &str, map: &mut BTreeMap<String, serde_json::Value>| {
        let mut parent = Path::new(rel).parent();
        while let Some(dir) = parent {
            let s = dir.to_str().unwrap_or("");
            if s.is_empty() {
                break;
            }
            map.entry(s.to_string()).or_insert_with(|| {
                serde_json::json!({"path": s, "kind": "directory", "mode": 0o755, "sha256": null, "size": null, "target": null})
            });
            parent = dir.parent();
        }
    };
    for (path, bytes) in executables {
        let (sha256, size) = (hex::encode(Sha256::digest(bytes)), bytes.len() as u64);
        exec_rows.push(serde_json::json!({"path": path, "sha256": sha256, "size": size}));
        if let Ok(rel) = path.strip_prefix(root) {
            let rel_str = rel.to_str().unwrap().replace('\\', "/");
            add_parents(&rel_str, &mut payload_rows);
            payload_rows.insert(
                rel_str.clone(),
                serde_json::json!({"path": rel_str, "kind": "file", "mode": 0o755, "sha256": sha256, "size": size, "target": null}),
            );
        }
    }
    for (rel_str, bytes, mode) in extra_files {
        add_parents(rel_str, &mut payload_rows);
        let sha256 = hex::encode(Sha256::digest(bytes));
        payload_rows.insert(
            (*rel_str).to_string(),
            serde_json::json!({"path": rel_str, "kind": "file", "mode": mode, "sha256": sha256, "size": bytes.len() as u64, "target": null}),
        );
    }
    if payload_rows.is_empty() {
        payload_rows.insert(
            "bin".to_string(),
            serde_json::json!({"path": "bin", "kind": "directory", "mode": 0o755, "sha256": null, "size": null, "target": null}),
        );
    }
    let payload: Vec<_> = payload_rows.into_values().collect();
    serde_json::json!({
        "format_version": FORMAT_VERSION, "package": PACKAGE_NAME, "version": "1.0.2",
        "architecture": architecture, "install_root": root, "executables": exec_rows, "payload": payload
    })
}
