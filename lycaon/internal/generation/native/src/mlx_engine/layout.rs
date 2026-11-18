use super::{Result, weights::quantization_bits};
use serde_json::Value;
use std::{collections::BTreeMap, path::Path};

pub fn validate(path: &Path, quantization: &Value, profile: crate::profile::Profile) -> Result<()> {
    let (mut tensors, _) = crate::tensor_inventory::read_header(path)?;
    tensors.remove("__metadata__");
    if tensors.keys().all(|name| name.starts_with("model.")) {
        tensors = tensors
            .into_iter()
            .map(|(name, tensor)| (format!("language_model.{name}"), tensor))
            .collect();
    }
    validate_tensors(tensors, quantization, profile)
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
        return Err(format!("Invalid model tensor layout: {name}").into());
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
    projection(tensors, quantization, name, &[], output, input)
}

fn projection(
    tensors: &mut BTreeMap<String, Value>,
    quantization: &Value,
    name: &str,
    batch: &[usize],
    output: usize,
    input: usize,
) -> Result<()> {
    let shape = |width| [batch, &[output, width]].concat();
    let prefix = format!("language_model.{name}");
    let weight = format!("{prefix}.weight");
    if tensors
        .get(&weight)
        .is_some_and(|tensor| tensor["dtype"] == "F16")
    {
        return take(tensors, &weight, &shape(input), "F16");
    }
    let bits = quantization_bits(quantization, name)? as usize;
    take(tensors, &weight, &shape(input * bits / 32), "U32")?;
    for suffix in ["scales", "biases"] {
        take(
            tensors,
            &format!("{prefix}.{suffix}"),
            &shape(input / 64),
            "F16",
        )?;
    }
    Ok(())
}

fn validate_tensors(
    mut tensors: BTreeMap<String, Value>,
    quantization: &Value,
    profile: crate::profile::Profile,
) -> Result<()> {
    let h = profile.hidden;
    let m = profile.intermediate;
    linear(&mut tensors, quantization, "model.embed_tokens", 262144, h)?;
    take(
        &mut tensors,
        "language_model.model.norm.weight",
        &[h],
        "F16",
    )?;
    if profile.has_ple() {
        linear(
            &mut tensors,
            quantization,
            "model.embed_tokens_per_layer",
            262144,
            10752,
        )?;
        linear(
            &mut tensors,
            quantization,
            "model.per_layer_model_projection",
            10752,
            h,
        )?;
        take(
            &mut tensors,
            "language_model.model.per_layer_projection_norm.weight",
            &[256],
            "F16",
        )?;
    }
    for layer in 0..profile.layers {
        let prefix = format!("model.layers.{layer}");
        let sliding = layer % 6 != 5;
        let dimension = if sliding { 256 } else { 512 };
        let mut norms = vec![
            ("input_layernorm", h),
            ("post_attention_layernorm", h),
            ("pre_feedforward_layernorm", h),
            ("post_feedforward_layernorm", h),
            ("self_attn.q_norm", dimension),
        ];
        let mut linears = vec![
            ("self_attn.q_proj", profile.attention_heads * dimension, h),
            ("self_attn.o_proj", h, profile.attention_heads * dimension),
            ("mlp.gate_proj", m, h),
            ("mlp.up_proj", m, h),
            ("mlp.down_proj", h, m),
        ];
        if layer < profile.kv_layers() {
            norms.push(("self_attn.k_norm", dimension));
            let kv_width = profile.kv_heads(sliding) * dimension;
            linears.push(("self_attn.k_proj", kv_width, h));
            if sliding || profile.has_ple() {
                linears.push(("self_attn.v_proj", kv_width, h));
            }
        }
        if profile.has_ple() {
            norms.push(("post_per_layer_input_norm", h));
            linears.extend([
                ("per_layer_input_gate", 256, h),
                ("per_layer_projection", h, 256),
            ]);
        }
        if profile.experts != 0 {
            norms.extend([
                ("post_feedforward_layernorm_1", h),
                ("post_feedforward_layernorm_2", h),
                ("pre_feedforward_layernorm_2", h),
            ]);
            linears.push(("router.proj", profile.experts, h));
            for (name, size) in [
                ("router.scale", h),
                ("router.per_expert_scale", profile.experts),
            ] {
                take(
                    &mut tensors,
                    &format!("language_model.{prefix}.{name}"),
                    &[size],
                    "F16",
                )?;
            }
            for (name, output, input) in [
                ("gate_proj", 704, h),
                ("up_proj", 704, h),
                ("down_proj", h, 704),
            ] {
                projection(
                    &mut tensors,
                    quantization,
                    &format!("{prefix}.experts.switch_glu.{name}"),
                    &[profile.experts],
                    output,
                    input,
                )?;
            }
        }
        take(
            &mut tensors,
            &format!("language_model.{prefix}.layer_scalar"),
            &[1],
            "F16",
        )?;
        for (name, size) in norms {
            take(
                &mut tensors,
                &format!("language_model.{prefix}.{name}.weight"),
                &[size],
                "F16",
            )?;
        }
        for (name, out, input) in linears {
            linear(
                &mut tensors,
                quantization,
                &format!("{prefix}.{name}"),
                out,
                input,
            )?;
        }
    }
    if !tensors.is_empty() {
        return Err("Unexpected tensors in text model artifact".into());
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
