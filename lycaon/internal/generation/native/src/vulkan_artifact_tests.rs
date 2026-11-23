use super::*;
use crate::profile::{DENSE, E4B, MOE, Profile};
use std::io::Write;

fn string_field(raw: &mut Vec<u8>, text: &str) {
    raw.extend((text.len() as u64).to_le_bytes());
    raw.extend(text.as_bytes());
}

fn field(name: &str, ty: u32, data: &[u8]) -> Vec<u8> {
    let mut raw = Vec::new();
    string_field(&mut raw, name);
    raw.extend(ty.to_le_bytes());
    raw.extend(data);
    raw
}

fn array(ty: u32, length: usize, values: &[u8]) -> Vec<u8> {
    let mut raw = ty.to_le_bytes().to_vec();
    raw.extend((length as u64).to_le_bytes());
    raw.extend(values);
    raw
}

struct Fixture(std::path::PathBuf);

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_file(self.0.join("model.gguf")).unwrap();
        std::fs::remove_dir(&self.0).unwrap();
    }
}

fn artifact(profile: Profile, incorrect_heads: bool) -> Fixture {
    let dir = std::env::temp_dir().join(format!(
        "pw-gguf-{}-{}-{incorrect_heads}",
        std::process::id(),
        profile.layers
    ));
    std::fs::create_dir(&dir).unwrap();
    let mut architecture = Vec::new();
    string_field(&mut architecture, "gemma4");
    let mut fields = vec![field("general.architecture", 8, &architecture)];
    for (key, value) in [
        ("block_count", profile.layers),
        ("embedding_length", profile.hidden),
        ("feed_forward_length", profile.intermediate),
        ("attention.head_count", profile.attention_heads),
        ("attention.shared_kv_layers", profile.shared_layers),
        ("attention.sliding_window", profile.window),
        (
            "embedding_length_per_layer_input",
            if profile.has_ple() { 256 } else { 0 },
        ),
        ("attention.key_length", 512),
        ("attention.value_length", 512),
        ("attention.key_length_swa", 256),
        ("attention.value_length_swa", 256),
    ] {
        fields.push(field(
            &format!("gemma4.{key}"),
            4,
            &(value as u32).to_le_bytes(),
        ));
    }
    let sliding: Vec<u8> = (0..profile.layers).map(|i| u8::from(i % 6 != 5)).collect();
    fields.push(field(
        "gemma4.attention.sliding_window_pattern",
        9,
        &array(7, profile.layers, &sliding),
    ));
    if profile.has_ple() {
        fields.push(field(
            "gemma4.attention.head_count_kv",
            4,
            &(if incorrect_heads { 3_u32 } else { 2 }).to_le_bytes(),
        ));
    } else {
        let heads: Vec<u8> = sliding
            .iter()
            .flat_map(|&s| {
                let n = if incorrect_heads {
                    42
                } else {
                    profile.kv_heads(s == 1)
                };
                (n as i32).to_le_bytes()
            })
            .collect();
        fields.push(field(
            "gemma4.attention.head_count_kv",
            9,
            &array(5, profile.layers, &heads),
        ));
    }
    if profile.experts != 0 {
        for (key, value) in [
            ("expert_count", 128_u32),
            ("expert_used_count", 8),
            ("expert_feed_forward_length", 704),
        ] {
            fields.push(field(&format!("gemma4.{key}"), 4, &value.to_le_bytes()));
        }
    }
    let mut raw = b"GGUF".to_vec();
    raw.extend(3_u32.to_le_bytes());
    raw.extend((if profile.has_ple() { 2_u64 } else { 1 }).to_le_bytes());
    raw.extend((fields.len() as u64).to_le_bytes());
    for metadata in fields {
        raw.extend(metadata);
    }
    string_field(&mut raw, "output_norm.weight");
    raw.extend(1_u32.to_le_bytes());
    raw.extend(8_u64.to_le_bytes());
    raw.extend(0_u32.to_le_bytes());
    raw.extend(0_u64.to_le_bytes());
    let mut payload = 32;
    if profile.has_ple() {
        string_field(&mut raw, "per_layer_token_embd.weight");
        raw.extend(2_u32.to_le_bytes());
        raw.extend(10752_u64.to_le_bytes());
        raw.extend(262144_u64.to_le_bytes());
        raw.extend(2_u32.to_le_bytes()); // GGML_TYPE_Q4_0
        raw.extend(32_u64.to_le_bytes());
        payload += 10752_u64 * 262144 / 32 * 18;
    }
    raw.resize(raw.len().div_ceil(32) * 32, 0);
    let mut file = std::fs::File::create(dir.join("model.gguf")).unwrap();
    file.write_all(&raw).unwrap();
    file.set_len(raw.len() as u64 + payload).unwrap();
    Fixture(dir)
}

#[test]
fn gguf_metadata_selects_each_profile_and_rejects_inconsistent_kv_heads() {
    for profile in [E4B, DENSE, MOE] {
        let good = artifact(profile, false);
        let plan = Vulkan::plan(&good.0, 128, 16).unwrap();
        assert_eq!(plan.mapped_ple_bytes != 0, profile.has_ple());
        let bad = artifact(profile, true);
        assert!(Vulkan::plan(&bad.0, 128, 16).is_err());
    }
}
