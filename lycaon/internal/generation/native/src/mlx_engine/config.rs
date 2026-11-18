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

pub fn profile(config: &Value) -> Result<crate::profile::Profile> {
    let text = config.get("text_config").unwrap_or(config);
    crate::profile::Profile::from_layers(
        text["num_hidden_layers"].as_u64().unwrap_or_default() as usize
    )
    .ok_or_else(|| "Unsupported Gemma 4 layer count".into())
}

pub fn validate(config: &Value) -> Result<()> {
    let text = config.get("text_config").unwrap_or(config);
    let profile = profile(config)?;
    for (name, value) in [
        ("hidden_size", profile.hidden),
        ("num_hidden_layers", profile.layers),
        ("num_key_value_heads", profile.sliding_heads),
        ("num_attention_heads", profile.attention_heads),
        ("num_kv_shared_layers", profile.shared_layers),
        ("sliding_window", profile.window),
        ("hidden_size_per_layer_input", profile.ple_width),
        ("vocab_size", 262144),
        ("vocab_size_per_layer_input", 262144),
        ("head_dim", 256),
        ("intermediate_size", profile.intermediate),
    ] {
        let optional_zero = matches!(name, "hidden_size_per_layer_input" | "num_kv_shared_layers");
        let actual = text.get(name).and_then(Value::as_u64);
        if actual != Some(value as u64)
            && !(optional_zero && value == 0 && text.get(name).is_none())
        {
            return Err(format!("Unsupported Gemma 4 {name}").into());
        }
    }
    if (!profile.has_ple()
        && text["num_global_key_value_heads"].as_u64() != Some(profile.global_heads as u64))
        || (profile.experts != 0
            && (text["num_experts"] != 128
                || text["top_k_experts"] != 8
                || text["moe_intermediate_size"] != 704))
        || text["enable_moe_block"] != (profile.experts != 0)
        || text["attention_bias"] != false
        || (!text["use_bidirectional_attention"].is_null()
            && text["use_bidirectional_attention"] != false
            && text["use_bidirectional_attention"] != "vision")
        || text["attention_k_eq_v"] != !profile.has_ple()
        || text["tie_word_embeddings"] != true
        || text["use_double_wide_mlp"] != false
        || text["hidden_activation"] != "gelu_pytorch_tanh"
        || !(text["model_type"] == "gemma4_text"
            || (profile == crate::profile::DENSE && text["model_type"] == "gemma4_unified_text"))
        || text["rms_norm_eps"].as_f64() != Some(1e-6)
        || text["final_logit_softcapping"].as_f64() != Some(30.0)
        || text
            .get("global_head_dim")
            .is_some_and(|v| v.as_u64() != Some(512))
    {
        return Err("Unsupported Gemma 4 architecture".into());
    }
    if let Some(overrides) = text.get("per_layer_config") {
        let overrides = overrides
            .as_object()
            .ok_or("Invalid per-layer configuration")?;
        for (key, value) in overrides {
            let layer: usize = key.parse()?;
            if layer >= profile.layers
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
    if layers.len() != profile.layers
        || layers.iter().enumerate().any(|(i, kind)| {
            kind.as_str()
                != Some(if i % 6 == 5 {
                    "full_attention"
                } else {
                    "sliding_attention"
                })
        })
    {
        return Err("Unsupported Gemma 4 attention layout".into());
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
        return Err("Unsupported Gemma 4 rotary configuration".into());
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
    fn dense_and_expert_students_validate_their_kv_and_routing_geometry() {
        for (layers, hidden, global_heads, intermediate, moe) in
            [(48, 3840, 1, 15360, false), (30, 2816, 2, 2112, true)]
        {
            let mut config = supported();
            let text = config["text_config"].as_object_mut().unwrap();
            text.remove("per_layer_config");
            text.remove("num_kv_shared_layers");
            text.remove("hidden_size_per_layer_input");
            for (key, value) in [
                ("num_hidden_layers", layers),
                ("hidden_size", hidden),
                ("num_global_key_value_heads", global_heads),
                ("num_key_value_heads", 8),
                ("num_attention_heads", 16),
                ("intermediate_size", intermediate),
                ("sliding_window", 1024),
            ] {
                text.insert(key.into(), value.into());
            }
            text.insert("enable_moe_block".into(), moe.into());
            text.insert("attention_k_eq_v".into(), true.into());
            text.insert("use_bidirectional_attention".into(), "vision".into());
            text.insert(
                "model_type".into(),
                if moe {
                    "gemma4_text"
                } else {
                    "gemma4_unified_text"
                }
                .into(),
            );
            text.insert(
                "layer_types".into(),
                (0..layers)
                    .map(|i| {
                        if i % 6 == 5 {
                            "full_attention"
                        } else {
                            "sliding_attention"
                        }
                    })
                    .collect::<Vec<_>>()
                    .into(),
            );
            if moe {
                for (key, value) in [
                    ("num_experts", 128),
                    ("top_k_experts", 8),
                    ("moe_intermediate_size", 704),
                ] {
                    text.insert(key.into(), value.into());
                }
            }
            validate(&config).unwrap();
            config["text_config"]["num_global_key_value_heads"] = 8.into();
            assert!(validate(&config).is_err());
        }
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
