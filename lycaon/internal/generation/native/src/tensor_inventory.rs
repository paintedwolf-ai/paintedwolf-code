use crate::error::{Error, Result};
use serde::Deserialize;
use std::{collections::BTreeMap, fs::File, io::Read, path::Path};

#[derive(Deserialize)]
struct Tensor {
    dtype: String,
    shape: Vec<u64>,
    data_offsets: [u64; 2],
}

pub fn safetensors(path: &Path) -> Result<(u64, u64)> {
    let mut file = File::open(path)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() || metadata.len() > 64 << 30 {
        return Err(invalid());
    }
    let mut size = [0; 8];
    file.read_exact(&mut size)?;
    let size = u64::from_le_bytes(size);
    if size > 16 << 20 || size + 8 > metadata.len() {
        return Err(invalid());
    }
    let mut header = vec![0; size as usize];
    file.read_exact(&mut header)?;
    inventory(&header, metadata.len() - size - 8)
}

fn inventory(header: &[u8], payload: u64) -> Result<(u64, u64)> {
    let mut values: BTreeMap<String, serde_json::Value> =
        serde_json::from_slice(header).map_err(|_| invalid())?;
    values.remove("__metadata__");
    if values.is_empty() || values.len() > 2048 {
        return Err(invalid());
    }
    let mut ranges = Vec::new();
    let (mut resident, mut mapped, mut ple_count) = (0, 0, 0);
    for (name, value) in values {
        let tensor: Tensor = serde_json::from_value(value).map_err(|_| invalid())?;
        let width = match tensor.dtype.as_str() {
            "U32" | "F32" => 4,
            "F16" | "BF16" => 2,
            _ => return Err(invalid()),
        };
        if tensor.shape.is_empty() || tensor.shape.len() > 4 {
            return Err(invalid());
        }
        let bytes = tensor
            .shape
            .iter()
            .try_fold(
                width,
                |bytes: u64, n| {
                    if *n == 0 { None } else { bytes.checked_mul(*n) }
                },
            )
            .ok_or_else(invalid)?;
        let [begin, end] = tensor.data_offsets;
        if end > payload || end.checked_sub(begin) != Some(bytes) {
            return Err(invalid());
        }
        ranges.push([begin, end]);
        if name.starts_with("language_model.model.embed_tokens_per_layer.") {
            ple_count += 1;
            mapped += bytes;
        } else {
            resident += bytes.div_ceil(16384) * 16384;
        }
    }
    ranges.sort_unstable();
    if ple_count != 3 || ranges.windows(2).any(|r| r[0][1] > r[1][0]) {
        return Err(invalid());
    }
    Ok((resident, mapped))
}

fn invalid() -> Error {
    Error::Checkpoint("invalid tensor inventory".into())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn rejects_oversized_header_before_reading_payload() {
        use std::io::Write;
        let path = std::env::temp_dir().join(format!(
            "paintedwolf-inventory-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&path)
            .unwrap();
        file.write_all(&((16_u64 << 20) + 1).to_le_bytes()).unwrap();
        file.set_len(32 << 20).unwrap();
        let result = safetensors(&path);
        std::fs::remove_file(path).unwrap();
        assert!(matches!(result, Err(Error::Checkpoint(_))));
    }

    #[test]
    fn counts_resident_padding_and_rejects_overlap_and_overflow() {
        let mut values = serde_json::Map::new();
        values.insert(
            "language_model.model.weight".into(),
            json!({"dtype":"F16","shape":[2],"data_offsets":[0,4]}),
        );
        for (i, suffix) in ["weight", "scales", "biases"].iter().enumerate() {
            values.insert(
                format!("language_model.model.embed_tokens_per_layer.{suffix}"),
                json!({"dtype":"F16","shape":[2],"data_offsets":[4+i*4,8+i*4]}),
            );
        }
        assert_eq!(
            inventory(&serde_json::to_vec(&values).unwrap(), 16).unwrap(),
            (16384, 12)
        );
        values.get_mut("language_model.model.weight").unwrap()["data_offsets"] = json!([4, 8]);
        assert!(inventory(&serde_json::to_vec(&values).unwrap(), 16).is_err());
        values.get_mut("language_model.model.weight").unwrap()["shape"] = json!([u64::MAX, 4]);
        assert!(inventory(&serde_json::to_vec(&values).unwrap(), 16).is_err());
    }
}
