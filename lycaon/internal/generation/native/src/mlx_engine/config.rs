use super::Result;
use serde_json::Value;

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
