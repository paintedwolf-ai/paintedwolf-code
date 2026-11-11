use std::collections::{HashMap, HashSet};

use serde_json::Value;

use crate::error::{Error, Result};

pub const ORDERED_TEMPLATE: &str = include_str!("../../templates/gemma4-code-v1.jinja");

pub enum ConversationFormat {
    Canonical,
    OrderedV1,
}

impl ConversationFormat {
    pub fn load(config: &Value, template: &str) -> Result<Self> {
        match config.get("paintedwolf_conversation_format") {
            None => Ok(Self::Canonical),
            Some(Value::String(name)) if name == "gemma4-code-v1" && template == ORDERED_TEMPLATE => {
                Ok(Self::OrderedV1)
            }
            _ => Err(Error::Tokenizer(
                "unsupported conversation format or template".into(),
            )),
        }
    }

    pub fn validate(&self, messages: &[Value]) -> Result<()> {
        if matches!(self, Self::OrderedV1) {
            return Ok(());
        }
        let mut calls = HashMap::new();
        let mut results = HashSet::new();
        for message in messages {
            if message["role"] == "tool" {
                let id = message["tool_call_id"].as_str().unwrap_or("");
                let name = message["name"].as_str().unwrap_or("");
                if id.is_empty() || calls.get(id) != Some(&name) || !results.insert(id) {
                    return Err(Error::Protocol(
                        "artifact template requires adjacent, uniquely paired tool results".into(),
                    ));
                }
                continue;
            }
            if !calls.is_empty() && results.is_empty() {
                return Err(Error::Protocol(
                    "artifact template requires a result after tool calls".into(),
                ));
            }
            calls.clear();
            results.clear();
            if let Some(items) = message["tool_calls"].as_array() {
                for call in items {
                    let id = call["id"].as_str().unwrap_or("");
                    let name = call["function"]["name"].as_str().unwrap_or("");
                    if id.is_empty() || name.is_empty() || calls.insert(id, name).is_some() {
                        return Err(Error::Protocol(
                            "tool history requires unique call identities".into(),
                        ));
                    }
                }
            }
        }
        if !calls.is_empty() && results.is_empty() {
            return Err(Error::Protocol(
                "artifact template cannot continue an unanswered tool call".into(),
            ));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn canonical_template_refuses_lossy_tool_histories() {
        let call = json!({"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"read"}}]});
        let result = json!({"role":"tool","tool_call_id":"c1","name":"read","content":"source"});
        let user = json!({"role":"user","content":"wait"});
        ConversationFormat::Canonical
            .validate(&[call.clone(), result.clone(), user.clone()])
            .unwrap();
        for messages in [
            vec![result.clone()],
            vec![call.clone()],
            vec![call.clone(), user, result.clone()],
            vec![call.clone(), result.clone(), result.clone()],
            vec![
                call,
                json!({"role":"tool","tool_call_id":"c1","name":"write"}),
            ],
        ] {
            assert!(ConversationFormat::Canonical.validate(&messages).is_err());
            ConversationFormat::OrderedV1.validate(&messages).unwrap();
        }
    }

    #[test]
    fn ordered_format_requires_its_exact_template() {
        let config = json!({"paintedwolf_conversation_format":"gemma4-code-v1"});
        assert!(matches!(
            ConversationFormat::load(&config, ORDERED_TEMPLATE).unwrap(),
            ConversationFormat::OrderedV1
        ));
        assert!(ConversationFormat::load(&config, "changed").is_err());
        assert!(ConversationFormat::load(
            &json!({"paintedwolf_conversation_format":"unknown"}),
            ORDERED_TEMPLATE
        )
        .is_err());
    }
}
