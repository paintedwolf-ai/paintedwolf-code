//! Head files: the weights one training run changes over the frozen backbone.
//!
//! A head file is a safetensors file whose tensors are the checkpoint's own `head.layers.*`,
//! `scorer.*`, `type_emb.*` and (optionally) `act_head.*` entries, and whose header metadata
//! names the training backbone. Loading requires a matching backbone.

use std::collections::HashMap;
use std::io::Read;
use std::path::Path;

use candle_core::{Device, Tensor};
use serde_json::Value;

use crate::error::{Error, Result};

/// The metadata key naming the encoder a head was trained over.
pub const META_BACKBONE: &str = "backbone";
/// The metadata key carrying the head's display label.
pub const META_LABEL: &str = "label";
/// The metadata key naming the checkpoint (hub id) the head was trained against.
pub const META_MODEL: &str = "model";

/// A head file read from disk, tensors still on the CPU.
#[derive(Debug)]
pub struct HeadFile {
    pub label: String,
    pub backbone: String,
    pub model: String,
    pub tensors: HashMap<String, Tensor>,
}

/// What a head file's header says about it.
#[derive(Debug, Clone)]
pub struct HeadMeta {
    pub label: String,
    pub backbone: String,
    pub model: String,
    pub tool_encoding: Option<String>,
}

/// Read a head file's header alone: enough to match it to a checkpoint before loading weights.
pub fn read_metadata_of(path: &Path) -> Result<HeadMeta> {
    let meta = read_metadata(path)?;
    let label = meta
        .get(META_LABEL)
        .cloned()
        .filter(|s| !s.is_empty())
        .or_else(|| path.file_stem().map(|s| s.to_string_lossy().into_owned()))
        .unwrap_or_default();
    let backbone = meta.get(META_BACKBONE).cloned().unwrap_or_default();
    if backbone.is_empty() {
        return Err(Error::Head(format!(
            "{}: the header names no backbone, so the head cannot be matched to a checkpoint",
            path.display()
        )));
    }
    let tool_encoding = meta.get("tool_encoding").cloned();
    if tool_encoding.as_ref().is_some_and(|v| v != "joint" && v != "independent") {
        return Err(Error::Head(format!("{}: unknown tool encoding {tool_encoding:?}", path.display())));
    }
    Ok(HeadMeta { label, backbone, model: meta.get(META_MODEL).cloned().unwrap_or_default(), tool_encoding })
}

/// Read a head file and its metadata.
pub fn read(path: &Path) -> Result<HeadFile> {
    let meta = read_metadata_of(path)?;
    let tensors = candle_core::safetensors::load(path, &Device::Cpu)?;
    Ok(HeadFile { label: meta.label, backbone: meta.backbone, model: meta.model, tensors })
}

/// The `__metadata__` map of a safetensors header, empty when the file carries none.
fn read_metadata(path: &Path) -> Result<HashMap<String, String>> {
    let mut file = std::fs::File::open(path).map_err(|e| Error::io(path.display(), e))?;
    let mut len = [0u8; 8];
    file.read_exact(&mut len).map_err(|e| Error::io(path.display(), e))?;
    let len = u64::from_le_bytes(len);
    // A header past this size is not a safetensors file; refuse before allocating for it.
    if len > 100 << 20 {
        return Err(Error::Head(format!("{}: safetensors header of {len} bytes", path.display())));
    }
    let mut header = vec![0u8; len as usize];
    file.read_exact(&mut header).map_err(|e| Error::io(path.display(), e))?;
    let header: Value =
        serde_json::from_slice(&header).map_err(|e| Error::json(path.display(), e))?;
    let mut meta = HashMap::new();
    if let Some(Value::Object(map)) = header.get("__metadata__") {
        for (k, v) in map {
            if let Value::String(s) = v {
                meta.insert(k.clone(), s.clone());
            }
        }
    }
    Ok(meta)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::{scratch_dir, write_safetensors};
    use candle_core::DType;

    fn write(path: &Path, meta: Option<HashMap<String, String>>) {
        let t = Tensor::zeros(2, DType::F32, &Device::Cpu).unwrap();
        let tensors: HashMap<String, Tensor> =
            [("scorer.0.weight".to_string(), t)].into_iter().collect();
        write_safetensors(path, &tensors, meta.as_ref());
    }

    #[test]
    fn metadata_names_the_backbone_and_label() {
        let dir = scratch_dir("head-meta");
        let path = dir.join("h.safetensors");
        let meta: HashMap<String, String> =
            [(META_BACKBONE, "tiny"), (META_LABEL, "code-rank"), (META_MODEL, "org/model")]
                .into_iter()
                .map(|(k, v)| (k.to_string(), v.to_string()))
                .collect();
        write(&path, Some(meta));
        let head = read(&path).unwrap();
        assert_eq!(
            (head.label.as_str(), head.backbone.as_str(), head.model.as_str()),
            ("code-rank", "tiny", "org/model")
        );
        assert!(head.tensors.contains_key("scorer.0.weight"));
    }

    #[test]
    fn a_head_without_a_backbone_is_refused() {
        let dir = scratch_dir("head-nometa");
        let path = dir.join("h.safetensors");
        write(&path, None);
        let err = read(&path).unwrap_err();
        assert!(matches!(err, Error::Head(ref m) if m.contains("backbone")), "{err}");
    }

    #[test]
    fn the_label_falls_back_to_the_file_stem() {
        let dir = scratch_dir("head-stem");
        let path = dir.join("turn-load.safetensors");
        let meta: HashMap<String, String> =
            [(META_BACKBONE.to_string(), "tiny".to_string())].into_iter().collect();
        write(&path, Some(meta));
        assert_eq!(read(&path).unwrap().label, "turn-load");
    }
}
