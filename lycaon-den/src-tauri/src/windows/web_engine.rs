//! The web engine and version the shell renders Den with, as a structured fact.

use serde::Serialize;

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WebEnginePayload {
    pub engine: &'static str,
    /// The engine's own release, when it reports one.
    pub version: Option<String>,
}

fn engine() -> &'static str {
    if cfg!(target_os = "linux") {
        "webkitgtk"
    } else if cfg!(target_os = "macos") {
        "wkwebview"
    } else if cfg!(target_os = "windows") {
        "webview2"
    } else {
        "unknown"
    }
}

fn payload(version: Option<String>) -> WebEnginePayload {
    WebEnginePayload {
        engine: engine(),
        version: version.map(|v| v.trim().to_string()).filter(|v| !v.is_empty()),
    }
}

#[tauri::command]
pub fn den_web_engine() -> WebEnginePayload {
    payload(tauri::webview_version().ok())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_the_build_platforms_engine() {
        let expected = if cfg!(target_os = "linux") {
            "webkitgtk"
        } else if cfg!(target_os = "macos") {
            "wkwebview"
        } else if cfg!(target_os = "windows") {
            "webview2"
        } else {
            "unknown"
        };
        assert_eq!(payload(None).engine, expected);
    }

    #[test]
    fn drops_an_empty_version() {
        assert_eq!(payload(Some("  ".into())).version, None);
        assert_eq!(payload(Some("2.54.1\n".into())).version.as_deref(), Some("2.54.1"));
    }

    #[test]
    fn serializes_in_camel_case() {
        let json = serde_json::to_string(&payload(Some("2.50.4".into()))).unwrap();
        assert!(json.ends_with(r#""version":"2.50.4"}"#), "{json}");
    }
}
