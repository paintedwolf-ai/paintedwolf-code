use std::fs;
use std::path::{Path, PathBuf};

fn command_js_name(src: &str, func: &str) -> String {
    let Some(at) = src.find(&format!("fn {func}(")) else {
        return func.to_string();
    };
    let preceding = &src[at.saturating_sub(160)..at];
    let Some(attr) = preceding.rfind("#[tauri::command") else {
        return func.to_string();
    };
    let attr_text = &preceding[attr..];
    let Some(rename) = attr_text.find("rename = \"") else {
        return func.to_string();
    };
    let rest = &attr_text[rename + "rename = \"".len()..];
    match rest.find('"') {
        Some(end) => rest[..end].to_string(),
        None => func.to_string(),
    }
}

/// `commands.allow` entry → the permission identifier that grants it.
fn permission_by_command(perm_dir: &Path) -> std::collections::HashMap<String, String> {
    let mut out = std::collections::HashMap::new();
    let Ok(entries) = fs::read_dir(perm_dir) else {
        return out;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("toml") {
            continue;
        }
        let Ok(text) = fs::read_to_string(&path) else {
            continue;
        };
        let mut identifier: Option<String> = None;
        for line in text.lines() {
            let line = line.trim();
            if let Some(rest) = line.strip_prefix("identifier = \"") {
                identifier = rest.strip_suffix('"').map(str::to_string);
            } else if let Some(rest) = line.strip_prefix("commands.allow = [") {
                let Some(id) = identifier.clone() else {
                    continue;
                };
                for name in rest.trim_end_matches(']').split(',') {
                    let name = name.trim().trim_matches('"');
                    if !name.is_empty() {
                        out.insert(name.to_string(), id.clone());
                    }
                }
            }
        }
    }
    out
}

/// Verifies command permissions and the default capability stay aligned.
#[test]
fn every_invoke_command_is_reachable_from_the_default_capability() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let lib_src = fs::read_to_string(root.join("src/lib.rs")).expect("read lib.rs");

    let marker = "tauri::generate_handler![";
    let start = lib_src.find(marker).expect("generate_handler! block") + marker.len();
    let rest = &lib_src[start..];
    let end = rest.find(']').expect("generate_handler! close");

    let capabilities =
        fs::read_to_string(root.join("capabilities/default.json")).expect("read default.json");
    let allowed = permission_by_command(&root.join("permissions"));

    let mut unreachable = Vec::new();
    for line in rest[..end].lines() {
        let entry = line.trim().trim_end_matches(',');
        if entry.is_empty() || entry.starts_with("//") {
            continue;
        }
        let (module, func) = match entry.rsplit_once("::") {
            Some((module, func)) => (module, func),
            None => ("lib", entry),
        };
        let module_src = if module == "lib" {
            lib_src.clone()
        } else {
            fs::read_to_string(root.join(format!("src/{}.rs", module.replace("::", "/"))))
                .unwrap_or_else(|_| panic!("read src/{module}.rs for {entry}"))
        };
        let name = command_js_name(&module_src, func);
        match allowed.get(&name) {
            None => unreachable.push(format!("{name}: no permissions/*.toml declares it")),
            Some(id) => {
                if !capabilities.contains(&format!("\"{id}\"")) {
                    unreachable.push(format!(
                        "{name}: permission {id} is missing from capabilities/default.json"
                    ));
                }
            }
        }
    }
    assert!(
        unreachable.is_empty(),
        "commands callable from JS but denied at runtime: {unreachable:#?}"
    );
}

#[test]
fn badge_permission_is_main_window_only() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let permission = "allow-den-set-badge-count";
    let allowed = permission_by_command(&root.join("permissions"));
    assert_eq!(
        allowed.get("den_set_badge_count").map(String::as_str),
        Some(permission)
    );
    for entry in fs::read_dir(root.join("capabilities")).expect("read capabilities") {
        let path = entry.expect("capability entry").path();
        if path.extension().and_then(|ext| ext.to_str()) != Some("json") {
            continue;
        }
        let raw = fs::read_to_string(&path).expect("read capability");
        let capability: serde_json::Value =
            serde_json::from_str(&raw).expect("parse capability");
        let grants_badge = capability["permissions"]
            .as_array()
            .expect("permissions array")
            .iter()
            .any(|entry| {
                entry.as_str() == Some(permission)
                    || entry["identifier"].as_str() == Some(permission)
            });
        if path.file_name().and_then(|name| name.to_str()) == Some("default.json") {
            assert!(grants_badge);
            assert_eq!(capability["windows"], serde_json::json!(["main"]));
        } else {
            assert!(
                !grants_badge,
                "badge permission escaped main capability: {}",
                path.display()
            );
        }
    }
}

#[test]
fn durable_close_can_destroy_every_window_it_intercepts() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    for name in ["default.json", "items.json"] {
        let raw = fs::read_to_string(root.join("capabilities").join(name))
            .unwrap_or_else(|_| panic!("read {name}"));
        let capability: serde_json::Value =
            serde_json::from_str(&raw).unwrap_or_else(|_| panic!("parse {name}"));
        let permissions = capability["permissions"]
            .as_array()
            .unwrap_or_else(|| panic!("permissions array in {name}"));
        assert!(
            permissions
                .iter()
                .any(|permission| permission == "core:window:allow-destroy"),
            "{name} must allow the durable close handler to destroy its window"
        );
    }
}

/// Prevents process-wide home overrides in parallel tests.
#[test]
fn no_source_file_sets_home_env_var() {
    let src_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("src");
    let mut offenders = Vec::new();
    let mut directories = vec![src_dir];
    while let Some(directory) = directories.pop() {
        for entry in fs::read_dir(&directory).expect("read source directory") {
            let path = entry.expect("source directory entry").path();
            if path.is_dir() {
                directories.push(path);
                continue;
            }
            if path.extension().and_then(|e| e.to_str()) != Some("rs") {
                continue;
            }
            let body = fs::read_to_string(&path).expect("read source file");
            for var in ["HOME", "USERPROFILE"] {
                if body.contains(&format!("set_var(\"{var}\"")) {
                    offenders.push(format!("{} sets {var}", path.display()));
                }
            }
        }
    }
    assert!(
        offenders.is_empty(),
        "env vars that path resolution reads must not be mutated:\n  {}",
        offenders.join("\n  ")
    );
}
