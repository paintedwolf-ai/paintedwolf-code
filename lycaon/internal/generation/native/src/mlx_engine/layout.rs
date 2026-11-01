use super::{Result, weights::quantization_bits};
use serde_json::Value;
use std::{collections::BTreeMap, path::Path};

pub fn validate(path: &Path, quantization: &Value) -> Result<()> {
    let (mut tensors, _) = crate::tensor_inventory::read_header(path)?;
    tensors.remove("__metadata__");
    validate_tensors(tensors, quantization)
}

fn take(
    tensors: &mut BTreeMap<String, Value>,
    name: &str,
    shape: &[usize],
    dtype: &str,
) -> Result<()> {
    let tensor = tensors
        .remove(name)
        .ok_or_else(|| format!("Missing tensor {name}"))?;
    if tensor["dtype"] != dtype || tensor["shape"] != serde_json::json!(shape) {
        return Err(format!("Invalid E4B tensor layout: {name}").into());
    }
    Ok(())
}

fn linear(
    tensors: &mut BTreeMap<String, Value>,
    quantization: &Value,
    name: &str,
    output: usize,
    input: usize,
) -> Result<()> {
    let prefix = format!("language_model.{name}");
    let weight = format!("{prefix}.weight");
    if tensors
        .get(&weight)
        .is_some_and(|tensor| tensor["dtype"] == "F16")
    {
        return take(tensors, &weight, &[output, input], "F16");
    }
    let bits = quantization_bits(quantization, name)? as usize;
    take(tensors, &weight, &[output, input * bits / 32], "U32")?;
    for suffix in ["scales", "biases"] {
        take(
            tensors,
            &format!("{prefix}.{suffix}"),
            &[output, input / 64],
            "F16",
        )?;
    }
    Ok(())
}

fn validate_tensors(mut tensors: BTreeMap<String, Value>, quantization: &Value) -> Result<()> {
    for (name, output, input) in [
        ("model.embed_tokens", 262144, 2560),
        ("model.embed_tokens_per_layer", 262144, 10752),
        ("model.per_layer_model_projection", 10752, 2560),
    ] {
        linear(&mut tensors, quantization, name, output, input)?;
    }
    for (name, size) in [
        ("model.norm.weight", 2560),
        ("model.per_layer_projection_norm.weight", 256),
    ] {
        take(
            &mut tensors,
            &format!("language_model.{name}"),
            &[size],
            "F16",
        )?;
    }
    for layer in 0..42 {
        let prefix = format!("model.layers.{layer}");
        let dimension = if layer % 6 == 5 { 512 } else { 256 };
        for name in [
            "input_layernorm",
            "post_attention_layernorm",
            "pre_feedforward_layernorm",
            "post_feedforward_layernorm",
            "post_per_layer_input_norm",
        ] {
            take(
                &mut tensors,
                &format!("language_model.{prefix}.{name}.weight"),
                &[2560],
                "F16",
            )?;
        }
        take(
            &mut tensors,
            &format!("language_model.{prefix}.layer_scalar"),
            &[1],
            "F16",
        )?;
        take(
            &mut tensors,
            &format!("language_model.{prefix}.self_attn.q_norm.weight"),
            &[dimension],
            "F16",
        )?;
        for (name, output, input) in [
            ("self_attn.q_proj", 8 * dimension, 2560),
            ("self_attn.o_proj", 2560, 8 * dimension),
            ("mlp.gate_proj", 10240, 2560),
            ("mlp.up_proj", 10240, 2560),
            ("mlp.down_proj", 2560, 10240),
            ("per_layer_input_gate", 256, 2560),
            ("per_layer_projection", 2560, 256),
        ] {
            linear(
                &mut tensors,
                quantization,
                &format!("{prefix}.{name}"),
                output,
                input,
            )?;
        }
        if layer < 24 {
            take(
                &mut tensors,
                &format!("language_model.{prefix}.self_attn.k_norm.weight"),
                &[dimension],
                "F16",
            )?;
            for name in ["k_proj", "v_proj"] {
                linear(
                    &mut tensors,
                    quantization,
                    &format!("{prefix}.self_attn.{name}"),
                    2 * dimension,
                    2560,
                )?;
            }
        }
    }
    if !tensors.is_empty() {
        return Err("Unexpected tensors in text-only E4B artifact".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn packed_linears_require_matching_shapes_and_half_precision_scales() {
        let quantization = json!({"bits": 6, "group_size": 64, "mode": "affine"});
        let tensors: BTreeMap<String, Value> = [
            ("weight", json!({"dtype":"U32","shape":[256,480]})),
            ("scales", json!({"dtype":"F16","shape":[256,40]})),
            ("biases", json!({"dtype":"F16","shape":[256,40]})),
        ]
        .into_iter()
        .map(|(suffix, value)| (format!("language_model.linear.{suffix}"), value))
        .collect();
        let mut valid = tensors.clone();
        linear(&mut valid, &quantization, "linear", 256, 2560).unwrap();
        assert!(valid.is_empty());
        for (suffix, field, value) in [
            ("weight", "shape", json!([256, 320])),
            ("scales", "dtype", json!("F32")),
            ("biases", "shape", json!([1, 40])),
        ] {
            let mut invalid = tensors.clone();
            invalid
                .get_mut(&format!("language_model.linear.{suffix}"))
                .unwrap()[field] = value;
            assert!(linear(&mut invalid, &quantization, "linear", 256, 2560).is_err());
        }
    }
}
