use serde_json::{Map, Value};

/// A window replaces its own configuration without replaying peer snapshots.
pub fn merge_window_intents(current: Value, requested: &Value, label: &str) -> Result<Value, String> {
    let mut windows = if current.is_null() {
        Map::new()
    } else {
        current
            .get("byWindow")
            .and_then(Value::as_object)
            .cloned()
            .ok_or("invalid_saved_tree_configuration")?
    };
    let identity = format!("window:{label}");
    let next = requested
        .get("byWindow")
        .and_then(Value::as_object)
        .and_then(|values| values.get(&identity))
        .ok_or("missing_window_tree_configuration")?;
    if !next.get("byWorkspace").is_some_and(Value::is_object) {
        return Err("invalid_window_tree_configuration".into());
    }
    windows.insert(identity.clone(), next.clone());
    while windows.len() > 8 {
        let oldest = windows
            .iter()
            .filter(|(key, _)| *key != &identity)
            .min_by_key(|(_, value)| value.get("touchedAt").and_then(Value::as_u64).unwrap_or(0))
            .map(|(key, _)| key.clone());
        if let Some(oldest) = oldest {
            windows.remove(&oldest);
        }
    }
    Ok(serde_json::json!({ "byWindow": windows }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn stale_peer_snapshots_cannot_replace_another_window() {
        let current = json!({"byWindow": {
            "window:main": {"byWorkspace": {"a": {"revision": "one"}}},
            "window:peer": {"byWorkspace": {"b": {"revision": "new"}}}
        }});
        let requested = json!({"byWindow": {
            "window:main": {"byWorkspace": {"a": {"revision": "two"}}},
            "window:peer": {"byWorkspace": {"b": {"revision": "old"}}}
        }});
        let merged = merge_window_intents(current, &requested, "main").expect("merge window");
        assert_eq!(merged["byWindow"]["window:main"]["byWorkspace"]["a"]["revision"], "two");
        assert_eq!(merged["byWindow"]["window:peer"]["byWorkspace"]["b"]["revision"], "new");
    }

    #[test]
    fn malformed_saved_shapes_are_refused() {
        let requested = json!({"byWindow": {"window:main": {"byWorkspace": {}}}});
        assert!(merge_window_intents(json!({"byWindow": []}), &requested, "main").is_err());
        assert!(merge_window_intents(Value::Null, &requested, "peer").is_err());
    }
}
