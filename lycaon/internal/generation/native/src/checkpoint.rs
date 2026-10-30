//! Safetensors loading and manifest validation.

use std::collections::HashMap;
use std::path::Path;

use crate::decoder::{Config, Decoder, Weights};
use crate::error::{Error, Result};

/// Read every f32 tensor from a safetensors file.
pub fn read_safetensors(path: &Path) -> Result<HashMap<String, Vec<f32>>> {
    let raw = std::fs::read(path).map_err(|e| Error::Checkpoint(format!("{}: {e}", path.display())))?;
    let mut cursor = 0usize;
    let read_u64 = |c: &mut usize| -> Result<usize> {
        let b = raw.get(*c..*c + 8).ok_or_else(|| Error::Checkpoint("truncated header".into()))?;
        *c += 8;
        Ok(u64::from_le_bytes(b.try_into().unwrap()) as usize)
    };
    let header_len = read_u64(&mut cursor)?;
    let header_end = cursor + header_len;
    let header: HashMap<String, serde_json::Value> = serde_json::from_slice(
        raw.get(cursor..header_end).ok_or_else(|| Error::Checkpoint("truncated header".into()))?,
    )
    .map_err(|e| Error::Checkpoint(format!("header: {e}")))?;
    let data_start = header_end;

    let mut out = HashMap::new();
    for (name, meta) in header {
        if name == "__metadata__" {
            continue;
        }
        let dtype = meta.get("dtype").and_then(|d| d.as_str()).unwrap_or_default();
        let shape: Vec<usize> = meta
            .get("shape")
            .and_then(|s| s.as_array())
            .map(|a| a.iter().filter_map(|v| v.as_u64().map(|v| v as usize)).collect())
            .unwrap_or_default();
        let offsets = meta
            .get("data_offsets")
            .and_then(|o| o.as_array())
            .map(|a| a.iter().filter_map(|v| v.as_u64().map(|v| v as usize)).collect::<Vec<_>>())
            .unwrap_or_default();
        let (start, end) = match offsets.as_slice() {
            [s, e] => (*s, *e),
            _ => return Err(Error::Checkpoint(format!("{name}: no data offsets"))),
        };
        let bytes = raw
            .get(data_start + start..data_start + end)
            .ok_or_else(|| Error::Checkpoint(format!("{name}: offsets out of range")))?;
        let elems: Vec<f32> = match dtype {
            "F32" => bytes.chunks_exact(4).map(|c| f32::from_le_bytes(c.try_into().unwrap())).collect(),
            "BF16" => bytes.chunks_exact(2).map(|c| f32::from_bits(f32_from_bf16(u16::from_le_bytes(c.try_into().unwrap())))).collect(),
            "F16" => bytes.chunks_exact(2).map(|c| half::f16::from_le_bytes(c.try_into().unwrap()).to_f32()).collect(),
            other => return Err(Error::Checkpoint(format!("{name}: dtype {other} not supported"))),
        };
        let _ = shape;
        out.insert(name, elems);
    }
    Ok(out)
}

fn f32_from_bf16(bits: u16) -> u32 {
    ((bits as u32) << 16) as u32
}

/// Load a decoder from a directory containing `config.json` and `model.safetensors`.
pub fn load_decoder(dir: &str, device: &str) -> Result<Decoder> {
    let dir = Path::new(dir);
    let cfg_path = dir.join("config.json");
    let cfg: serde_json::Value = serde_json::from_str(
        &std::fs::read_to_string(&cfg_path).map_err(|e| Error::Checkpoint(format!("{}: {e}", cfg_path.display())))?,
    )
    .map_err(|e| Error::Checkpoint(format!("config.json: {e}")))?;
    let cfg = Config::from_json(&cfg)?;

    let weights_path = dir.join("model.safetensors");
    let tensors = read_safetensors(&weights_path)?;
    if device == "mlx" && !cfg!(all(target_os = "macos", target_arch = "aarch64")) {
        return Err(Error::Device("mlx requested on a non-Apple-silicon host".into()));
    }
    Decoder::load(cfg, Weights { tensors })
}
