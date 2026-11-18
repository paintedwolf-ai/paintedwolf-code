use super::*;
use std::collections::HashMap;

fn tensor(shape: &[i32], value: f32) -> Array {
    ops::broadcast_to(
        Array::from_f32(value)
            .as_dtype(mlx_rs::Dtype::Float16)
            .unwrap(),
        shape,
    )
    .unwrap()
}

fn layer(profile: Profile, i: usize) -> Layer {
    let h = profile.hidden as i32;
    let m = profile.intermediate as i32;
    let sliding = i % 6 != 5;
    let d = if sliding { 256 } else { 512 };
    let prefix = format!("language_model.model.layers.{i}");
    let mut tensors = HashMap::new();
    let mut put = |name: &str, shape: &[i32], value| {
        tensors.insert(format!("{prefix}.{name}"), tensor(shape, value));
    };
    for name in [
        "input_layernorm",
        "post_attention_layernorm",
        "pre_feedforward_layernorm",
        "post_feedforward_layernorm",
    ] {
        put(&format!("{name}.weight"), &[h], 1.);
    }
    put("layer_scalar", &[1], 1.);
    for name in ["q_norm", "k_norm"] {
        put(&format!("self_attn.{name}.weight"), &[d], 1.);
    }
    for (name, output, input) in [
        ("self_attn.q_proj", 2 * d, h),
        ("self_attn.k_proj", d, h),
        ("self_attn.o_proj", h, 2 * d),
        ("mlp.gate_proj", m, h),
        ("mlp.up_proj", m, h),
        ("mlp.down_proj", h, m),
    ] {
        put(&format!("{name}.weight"), &[output, input], 0.001);
    }
    if sliding {
        put("self_attn.v_proj.weight", &[d, h], 0.001);
    }
    if profile.experts != 0 {
        for name in [
            "post_feedforward_layernorm_1.weight",
            "post_feedforward_layernorm_2.weight",
            "pre_feedforward_layernorm_2.weight",
            "router.scale",
        ] {
            put(name, &[h], 1.);
        }
        put("router.proj.weight", &[128, h], 0.001);
        put("router.per_expert_scale", &[128], 1.);
        for (name, output, input) in [
            ("gate_proj", 704, h),
            ("up_proj", 704, h),
            ("down_proj", h, 704),
        ] {
            put(
                &format!("experts.switch_glu.{name}.weight"),
                &[128, output, input],
                0.001,
            );
        }
    }
    Layer::load(&mut Weights::fixture(tensors), i, profile).unwrap()
}

#[test]
fn prefill_builds_every_nonshared_bank_for_dense_and_routed_models() {
    for architecture in [crate::profile::DENSE, crate::profile::MOE] {
        let profile = Profile {
            layers: 6,
            hidden: 64,
            intermediate: 64,
            attention_heads: 2,
            sliding_heads: 1,
            global_heads: 1,
            ..architecture
        };
        let model = Model {
            embedding: Linear {
                weight: tensor(&[128, 64], 0.01),
                quantization: None,
            },
            ple: None,
            profile,
            layers: (0..6).map(|i| layer(profile, i)).collect(),
            final_norm: tensor(&[64], 1.),
            activation: Activation::new().unwrap(),
        };
        let mut cache = Cache::new(4, profile).unwrap();
        assert!(
            model
                .forward(&[2, 3], &mut cache, Logits::None)
                .unwrap()
                .is_none()
        );
        cache.evaluate().unwrap();
        assert_eq!(cache.bytes(), 5 * 2 * 256 * 4 + 4 * 512 * 4);
        let next = model
            .forward(&[4, 5], &mut cache, Logits::Last)
            .unwrap()
            .unwrap();
        assert_eq!(next.shape(), &[1, 1, 128]);
        assert!(
            next.is_finite()
                .unwrap()
                .all(None)
                .unwrap()
                .try_item_exact::<bool>()
                .unwrap()
        );
    }
}
