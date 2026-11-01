use std::fs::File;
use std::io::{Read, Seek, SeekFrom};
use std::path::Path;

use half::f16;
use memmap2::{Mmap, MmapOptions};
use mlx_rs::{Array, ops};
use serde::Deserialize;
use std::collections::HashMap;

use super::Result;

#[derive(Deserialize)]
struct Tensor {
    dtype: String,
    shape: Vec<usize>,
    data_offsets: [usize; 2],
}

pub struct MappedPLE {
    mapped: Mmap,
    tensors: Vec<Tensor>,
    start: usize,
    bits: i32,
}

impl MappedPLE {
    pub fn open(path: &Path, bits: i32) -> Result<Self> {
        if ![4, 6, 8].contains(&bits) {
            return Err("Unsupported PLE precision".into());
        }
        let mut file = File::open(path)?;
        let metadata = file.metadata()?;
        if !metadata.is_file() || metadata.len() > 64 << 30 {
            return Err("Tensor mapping requires a bounded regular file".into());
        }
        let mut length = [0; 8];
        file.read_exact(&mut length)?;
        let length = u64::from_le_bytes(length);
        if length > 16 << 20 {
            return Err("Tensor header exceeds its bound".into());
        }
        let mut header = vec![0; length as usize];
        file.read_exact(&mut header)?;
        let mut values: HashMap<String, serde_json::Value> = serde_json::from_slice(&header)?;
        let start = 8 + length as usize;
        let mut tensors = Vec::new();
        for (suffix, dtype, width, bytes) in [
            ("weight", "U32", 10752 * bits as usize / 32, 4),
            ("scales", "F16", 168, 2),
            ("biases", "F16", 168, 2),
        ] {
            let name = format!("language_model.model.embed_tokens_per_layer.{suffix}");
            let tensor: Tensor =
                serde_json::from_value(values.remove(&name).ok_or("Missing PLE tensor")?)?;
            let [begin, end] = tensor.data_offsets;
            if tensor.dtype != dtype
                || tensor.shape != [262144, width]
                || end.checked_sub(begin) != Some(262144 * width * bytes)
                || end > (metadata.len() as usize).saturating_sub(start)
            {
                return Err("Invalid PLE tensor layout".into());
            }
            tensors.push(tensor);
        }
        let mut ranges: Vec<_> = tensors.iter().map(|t| t.data_offsets).collect();
        ranges.sort_unstable();
        if ranges.windows(2).any(|pair| pair[0][1] > pair[1][0]) {
            return Err("Overlapping PLE tensors".into());
        }
        file.seek(SeekFrom::Start(0))?;
        // The caller must keep the opened inode immutable while mapped.
        let mapped = unsafe { MmapOptions::new().map(&file)? };
        Ok(Self {
            mapped,
            tensors,
            start,
            bits,
        })
    }

    pub fn gather(&self, ids: &[i32]) -> Result<Array> {
        if ids.is_empty() || ids.len() > 256 || ids.iter().any(|id| !(0..262144).contains(id)) {
            return Err("PLE row gather exceeds its bounds".into());
        }
        let mut arrays = Vec::new();
        for (index, tensor) in self.tensors.iter().enumerate() {
            let width = tensor.shape[1];
            let stride = width * if index == 0 { 4 } else { 2 };
            let mut bytes = Vec::with_capacity(ids.len() * stride);
            for &id in ids {
                let begin = self.start + tensor.data_offsets[0] + id as usize * stride;
                bytes.extend_from_slice(&self.mapped[begin..begin + stride]);
            }
            let shape = [ids.len() as i32, width as i32];
            arrays.push(if index == 0 {
                Array::from_slice(
                    &bytes
                        .chunks_exact(4)
                        .map(|b| u32::from_le_bytes(b.try_into().unwrap()))
                        .collect::<Vec<_>>(),
                    &shape,
                )
            } else {
                Array::from_slice(
                    &bytes
                        .chunks_exact(2)
                        .map(|b| f16::from_bits(u16::from_le_bytes(b.try_into().unwrap())))
                        .collect::<Vec<_>>(),
                    &shape,
                )
            });
        }
        Ok(
            ops::dequantize(&arrays[0], &arrays[1], &arrays[2], 64, self.bits)?.reshape(&[
                1,
                ids.len() as i32,
                42,
                256,
            ])?,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicU64, Ordering};

    struct Fixture(PathBuf);

    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = std::fs::remove_file(&self.0);
        }
    }

    fn fixture(overlap: bool) -> Fixture {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let path = std::env::temp_dir().join(format!(
            "paintedwolf-ple-{}-{}", std::process::id(), NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let mut file = std::fs::OpenOptions::new().write(true).create_new(true).open(&path).unwrap();
        let mut header = serde_json::Map::new();
        let mut end = 0;
        let mut bias_offset = 0;
        for (suffix, dtype, width, bytes) in [
            ("weight", "U32", 1344, 4),
            ("scales", "F16", 168, 2),
            ("biases", "F16", 168, 2),
        ] {
            let begin = if overlap { 0 } else { end };
            end = begin + 262144_u64 * width * bytes;
            if suffix == "biases" { bias_offset = begin; }
            header.insert(format!("language_model.model.embed_tokens_per_layer.{suffix}"),
                serde_json::json!({"dtype": dtype, "shape": [262144, width], "data_offsets": [begin, end]}));
        }
        let header = serde_json::to_vec(&header).unwrap();
        file.write_all(&(header.len() as u64).to_le_bytes()).unwrap();
        file.write_all(&header).unwrap();
        let start = 8 + header.len() as u64;
        file.set_len(start + 262144 * 1344 * 4 + 2 * 262144 * 168 * 2).unwrap();
        file.seek(SeekFrom::Start(start + bias_offset + 7 * 168 * 2)).unwrap();
        for _ in 0..168 { file.write_all(&f16::from_f32(2.0).to_bits().to_le_bytes()).unwrap(); }
        Fixture(path)
    }

    #[test]
    fn sparse_mapping_gathers_selected_rows_without_materializing_table() {
        let fixture = fixture(false);
        let ple = MappedPLE::open(&fixture.0, 4).unwrap();
        let output = ple.gather(&[7, 0, 7]).unwrap().as_dtype(mlx_rs::Dtype::Float32).unwrap();
        output.eval().unwrap();
        let values = output.as_slice::<f32>();
        assert_eq!(values.len(), 3 * 10752);
        assert!(values[..10752].iter().all(|v| *v == 2.0));
        assert!(values[10752..21504].iter().all(|v| *v == 0.0));
        assert!(values[21504..].iter().all(|v| *v == 2.0));
        for ids in [vec![], vec![-1], vec![262144], vec![0; 257]] {
            assert!(ple.gather(&ids).is_err());
        }
    }

    #[test]
    fn rejects_overlapping_tensor_ranges() {
        let fixture = fixture(true);
        assert!(MappedPLE::open(&fixture.0, 4).is_err());
    }
}
