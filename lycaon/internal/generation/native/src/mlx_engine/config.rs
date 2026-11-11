use super::Result;
use serde_json::Value;

pub fn read(directory: &std::path::Path) -> Result<Value> {
    use std::io::Read;
    let file = std::fs::File::open(directory.join("config.json"))?;
    if !file.metadata()?.is_file() || file.metadata()?.len() > 4 << 20 {
        return Err("Model configuration exceeds its bound".into());
    }
    let value = serde_json::from_reader(file.take((4 << 20) + 1))?;
    validate(&value)?;
    Ok(value)
}

pub fn validate(config: &Value) -> Result<()> {
    let text = &config["text_config"];
    for (name, value) in [
        ("hidden_size", 2560),
        ("num_hidden_layers", 42),
        ("num_key_value_heads", 2),
        ("num_attention_heads", 8),
        ("num_kv_shared_layers", 18),
        ("sliding_window", 512),
        ("hidden_size_per_layer_input", 256),
        ("vocab_size", 262144),
        ("vocab_size_per_layer_input", 262144),
        ("head_dim", 256),
        ("intermediate_size", 10240),
    ] {
        if text[name].as_u64() != Some(value) {
            return Err(format!("Unsupported E4B {name}").into());
        }
    }
    if text["enable_moe_block"] != false
        || text["attention_bias"] != false
        || (!text["use_bidirectional_attention"].is_null()
            && text["use_bidirectional_attention"] != false)
        || text["attention_k_eq_v"] != false
        || text["tie_word_embeddings"] != true
        || text["use_double_wide_mlp"] != false
        || text["hidden_activation"] != "gelu_pytorch_tanh"
        || text["model_type"] != "gemma4_text"
        || text["rms_norm_eps"].as_f64() != Some(1e-6)
        || text["final_logit_softcapping"].as_f64() != Some(30.0)
        || text
            .get("global_head_dim")
            .is_some_and(|v| v.as_u64() != Some(512))
    {
        return Err("Unsupported E4B architecture".into());
    }
    if let Some(overrides) = text.get("per_layer_config") {
        let overrides = overrides
            .as_object()
            .ok_or("Invalid per-layer configuration")?;
        for (key, value) in overrides {
            let layer: usize = key.parse()?;
            if layer >= 42
                || value.as_object().is_none_or(|v| v.len() != 1)
                || value["head_dim"].as_u64() != Some(if layer % 6 == 5 { 512 } else { 256 })
            {
                return Err("Unsupported per-layer configuration".into());
            }
        }
    }
    let layers = text["layer_types"]
        .as_array()
        .ok_or("Missing attention layout")?;
    if layers.len() != 42
        || layers.iter().enumerate().any(|(i, kind)| {
            kind.as_str()
                != Some(if i % 6 == 5 {
                    "full_attention"
                } else {
                    "sliding_attention"
                })
        })
    {
        return Err("Unsupported E4B attention layout".into());
    }
    let rope = &text["rope_parameters"];
    if rope["sliding_attention"]["rope_theta"].as_f64() != Some(10000.0)
        || rope["sliding_attention"]["rope_type"] != "default"
        || rope["full_attention"]["rope_theta"].as_f64() != Some(1_000_000.0)
        || rope["full_attention"]["rope_type"] != "proportional"
        || rope["full_attention"]["partial_rotary_factor"].as_f64() != Some(0.25)
        || rope["full_attention"]
            .get("factor")
            .is_some_and(|v| v.as_f64() != Some(1.0))
    {
        return Err("Unsupported E4B rotary configuration".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn supported() -> Value {
        serde_json::json!({"text_config": {
            "hidden_size": 2560, "num_hidden_layers": 42, "num_key_value_heads": 2,
            "num_attention_heads": 8, "num_kv_shared_layers": 18, "sliding_window": 512,
            "hidden_size_per_layer_input": 256, "vocab_size": 262144,
            "vocab_size_per_layer_input": 262144, "head_dim": 256, "intermediate_size": 10240,
            "enable_moe_block": false, "attention_bias": false, "attention_k_eq_v": false,
            "tie_word_embeddings": true, "use_double_wide_mlp": false,
            "hidden_activation": "gelu_pytorch_tanh", "model_type": "gemma4_text",
            "rms_norm_eps": 1e-6, "final_logit_softcapping": 30.0,
            "layer_types": (0..42).map(|i| if i % 6 == 5 { "full_attention" } else { "sliding_attention" }).collect::<Vec<_>>(),
            "per_layer_config": {"05": {"head_dim": 512}},
            "rope_parameters": {
                "sliding_attention": {"rope_theta": 10000.0, "rope_type": "default"},
                "full_attention": {"rope_theta": 1000000.0, "rope_type": "proportional", "partial_rotary_factor": 0.25}
            }
        }})
    }

    #[test]
    fn rejects_architectural_changes_before_loading_weights() {
        let config = supported();
        validate(&config).unwrap();
        for (key, replacement) in [
            ("num_kv_shared_layers", serde_json::json!(0)),
            ("enable_moe_block", serde_json::json!(true)),
            ("use_bidirectional_attention", serde_json::json!("true")),
            (
                "per_layer_config",
                serde_json::json!({"05": {"head_dim": 256}}),
            ),
            ("layer_types", serde_json::json!(["full_attention"])),
        ] {
            let mut invalid = config.clone();
            invalid["text_config"][key] = replacement;
            assert!(validate(&invalid).is_err(), "accepted {key}");
        }
        let mut invalid = config;
        invalid["text_config"]["rope_parameters"]["full_attention"]["partial_rotary_factor"] =
            serde_json::json!(1.0);
        assert!(validate(&invalid).is_err());
    }
}
