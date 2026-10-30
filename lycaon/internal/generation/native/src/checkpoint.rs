//! Safetensors loading, bounded and device-validated.
//!
//! The loader streams each tensor from disk and converts only what it keeps, never
//! holding the raw file and the expanded float copy at once. Device names are closed:
//! `cpu` always; `mlx` only on Apple silicon. Anything else is refused before I/O.

use std::collections::HashMap;
use std::path::Path;

use crate::decoder::{Config, Decoder, Weights};
use crate::error::{Error, Result};

/// Upper bound on one staging read. The E4B token embedding stored as F32 is
/// about 2.5 GiB on its own, so the bound must clear it with slack; a corrupt
/// header still cannot drive allocation past the file's real size.
const MAX_TENSOR_BYTES: u64 = 4 << 30;

/// Device names this service accepts, and whether the host can serve them.
pub fn device_supported(device: &str) -> bool {
    match device {
        "cpu" => true,
        "mlx" => cfg!(all(target_os = "macos", target_arch = "aarch64")),
        _ => false,
    }
}

/// Read every tensor from a safetensors file as f32, streaming per tensor so the
/// raw bytes are never held alongside the whole expanded model.
pub fn read_safetensors(path: &Path) -> Result<HashMap<String, Vec<f32>>> {
    let file = std::fs::File::open(path).map_err(|e| Error::Checkpoint(format!("{}: {e}", path.display())))?;
    let file_size = file.metadata().map_err(|e| Error::Checkpoint(format!("{}: {e}", path.display())))?.len();
    let mut file = std::io::BufReader::new(file);

    let mut len_buf = [0u8; 8];
    std::io::Read::read_exact(&mut file, &mut len_buf)
        .map_err(|e| Error::Checkpoint(format!("{}: truncated header: {e}", path.display())))?;
    let header_len = u64::from_le_bytes(len_buf);
    if header_len > MAX_TENSOR_BYTES || header_len > file_size {
        return Err(Error::Checkpoint(format!("{}: implausible header of {header_len} bytes", path.display())));
    }
    let mut header_raw = vec![0u8; header_len as usize];
    std::io::Read::read_exact(&mut file, &mut header_raw)
        .map_err(|e| Error::Checkpoint(format!("{}: truncated header: {e}", path.display())))?;
    let header: HashMap<String, serde_json::Value> = serde_json::from_slice(&header_raw)
        .map_err(|e| Error::Checkpoint(format!("{}: header: {e}", path.display())))?;
    let data_start: u64 = 8 + header_len;

    let mut out = HashMap::new();
    for (name, meta) in header {
        if name == "__metadata__" {
            continue;
        }
        let dtype = meta.get("dtype").and_then(|d| d.as_str()).unwrap_or_default().to_string();
        let offsets = meta
            .get("data_offsets")
            .and_then(|o| o.as_array())
            .map(|a| a.iter().filter_map(|v| v.as_u64()).collect::<Vec<_>>())
            .unwrap_or_default();
        let (start, end) = match offsets.as_slice() {
            [s, e] => (*s, *e),
            _ => return Err(Error::Checkpoint(format!("{name}: no data offsets"))),
        };
        if end < start || end.saturating_sub(start) > MAX_TENSOR_BYTES || data_start + end > file_size {
            return Err(Error::Checkpoint(format!("{name}: offsets out of range")));
        }
        // Stream to the current position. Safetensors tensors are dense and in order,
        // but seek from the start regardless so a sparse header cannot desync reads.
        let elem_size: u64 = match dtype.as_str() {
            "F32" => 4,
            "F16" | "BF16" => 2,
            other => return Err(Error::Checkpoint(format!("{name}: dtype {other} not supported"))),
        };
        let count = (end - start) / elem_size;
        std::io::Seek::seek(&mut file, std::io::SeekFrom::Start(data_start + start))
            .map_err(|e| Error::Checkpoint(format!("{name}: seek: {e}")))?;
        let mut bytes = vec![0u8; (end - start) as usize];
        std::io::Read::read_exact(&mut file, &mut bytes)
            .map_err(|e| Error::Checkpoint(format!("{name}: truncated tensor: {e}")))?;
        let mut elems = Vec::with_capacity(count as usize);
        match dtype.as_str() {
            "F32" => elems.extend(bytes.chunks_exact(4).map(|c| f32::from_le_bytes(c.try_into().unwrap()))),
            "BF16" => elems.extend(bytes.chunks_exact(2).map(|c| f32::from_bits((u16::from_le_bytes(c.try_into().unwrap()) as u32) << 16))),
            "F16" => elems.extend(bytes.chunks_exact(2).map(|c| half::f16::from_le_bytes(c.try_into().unwrap()).to_f32())),
            _ => unreachable!("dtype checked above"),
        }
        out.insert(name, elems);
    }
    Ok(out)
}

/// Load a decoder from a directory containing `config.json` and `model.safetensors`.
pub fn load_decoder(dir: &str, device: &str) -> Result<Decoder> {
    if !device_supported(device) {
        return Err(Error::Device(format!("device {device:?} is not available on this host")));
    }
    let dir = Path::new(dir);
    let cfg_path = dir.join("config.json");
    let cfg: serde_json::Value = serde_json::from_str(
        &std::fs::read_to_string(&cfg_path).map_err(|e| Error::Checkpoint(format!("{}: {e}", cfg_path.display())))?,
    )
    .map_err(|e| Error::Checkpoint(format!("config.json: {e}")))?;
    let cfg = Config::from_json(&cfg)?;

    let weights_path = dir.join("model.safetensors");
    let tensors = read_safetensors(&weights_path)?;
    Decoder::load(cfg, Weights { tensors })
}
