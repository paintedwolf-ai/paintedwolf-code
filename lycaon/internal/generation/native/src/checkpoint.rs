//! Bounded safetensors checkpoint loading and device validation.

use std::collections::HashMap;
use std::path::Path;

use crate::decoder::{Config, Decoder, Weights};
use crate::error::{Error, Result};

const MAX_TENSOR_BYTES: u64 = 4 << 30;

pub fn device_supported(device: &str) -> bool {
    matches!(device, "cpu")
}

/// Stream tensors from a safetensors file into f32 vectors one tensor at a time.
pub fn read_safetensors(path: &Path) -> Result<HashMap<String, Vec<f32>>> {
    let file =
        std::fs::File::open(path).map_err(|e| Error::Checkpoint(format!("{}: {e}", path.display())))?;
    let file_size = file
        .metadata()
        .map_err(|e| Error::Checkpoint(format!("{}: {e}", path.display())))?
        .len();
    let mut file = std::io::BufReader::new(file);

    let mut len_buf = [0u8; 8];
    std::io::Read::read_exact(&mut file, &mut len_buf)
        .map_err(|e| Error::Checkpoint(format!("{}: truncated header: {e}", path.display())))?;
    let header_len = u64::from_le_bytes(len_buf);
    if header_len > MAX_TENSOR_BYTES || header_len > file_size {
        return Err(Error::Checkpoint(format!(
            "{}: implausible header of {header_len} bytes",
            path.display()
        )));
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
        let dtype = meta
            .get("dtype")
            .and_then(|d| d.as_str())
            .unwrap_or_default()
            .to_string();
        let offsets = meta
            .get("data_offsets")
            .and_then(|o| o.as_array())
            .map(|a| a.iter().filter_map(|v| v.as_u64()).collect::<Vec<_>>())
            .unwrap_or_default();
        let (start, end) = match offsets.as_slice() {
            [s, e] => (*s, *e),
            _ => return Err(Error::Checkpoint(format!("{name}: no data offsets"))),
        };
        if end < start
            || end.saturating_sub(start) > MAX_TENSOR_BYTES
            || data_start + end > file_size
        {
            return Err(Error::Checkpoint(format!("{name}: offsets out of range")));
        }
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
            "F32" => elems.extend(
                bytes
                    .chunks_exact(4)
                    .map(|c| f32::from_le_bytes(c.try_into().unwrap())),
            ),
            "BF16" => elems.extend(
                bytes
                    .chunks_exact(2)
                    .map(|c| f32::from_bits((u16::from_le_bytes(c.try_into().unwrap()) as u32) << 16)),
            ),
            "F16" => elems.extend(
                bytes
                    .chunks_exact(2)
                    .map(|c| half::f16::from_le_bytes(c.try_into().unwrap()).to_f32()),
            ),
            _ => unreachable!("dtype checked above"),
        }
        out.insert(name, elems);
    }
    Ok(out)
}

/// Load a decoder from a directory containing `config.json` and `model.safetensors`.
pub fn load_decoder(dir: &str, device: &str) -> Result<Decoder> {
    if !device_supported(device) {
        return Err(Error::Device(format!(
            "device {device:?} is not available on this host"
        )));
    }
    let dir = Path::new(dir);
    let cfg_path = dir.join("config.json");
    let cfg: serde_json::Value = serde_json::from_str(
        &std::fs::read_to_string(&cfg_path)
            .map_err(|e| Error::Checkpoint(format!("{}: {e}", cfg_path.display())))?,
    )
    .map_err(|e| Error::Checkpoint(format!("config.json: {e}")))?;
    let cfg = Config::from_json(&cfg)?;

    let weights_path = dir.join("model.safetensors");
    let tensors = read_safetensors(&weights_path)?;
    Decoder::load(cfg, Weights { tensors })
}

/// Load the checkpoint tokenizer from `tokenizer.json`.
pub fn load_tokenizer(dir: &str) -> Result<tokenizers::Tokenizer> {
    let tok_path = Path::new(dir).join("tokenizer.json");
    if !tok_path.is_file() {
        return Err(Error::Tokenizer(format!(
            "{}: missing tokenizer.json",
            tok_path.display()
        )));
    }
    tokenizers::Tokenizer::from_file(&tok_path).map_err(|e| Error::Tokenizer(e.to_string()))
}
