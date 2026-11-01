use super::Result;
use mlx_rs::{Array, Dtype, ops, ops::indexing::*};

#[derive(Clone)]
pub struct KV {
    pub keys: Array,
    pub values: Array,
}

#[derive(Clone)]
pub struct Cache {
    pub offset: i32,
    layers: Vec<Option<KV>>,
}

impl Default for Cache {
    fn default() -> Self {
        Self {
            offset: 0,
            layers: (0..24).map(|_| None).collect(),
        }
    }
}

impl Cache {
    pub fn update(&mut self, layer: usize, keys: Array, values: Array) -> Result<KV> {
        let shape = keys.shape();
        let count = shape.get(2).copied().unwrap_or(0);
        let dimension = if layer % 6 == 5 { 512 } else { 256 };
        if layer >= 24
            || !(1..=256).contains(&count)
            || !(0..=65536 - count).contains(&self.offset)
            || shape != [1, 2, count, dimension]
            || values.shape() != shape
            || keys.dtype() != Dtype::Float16
            || values.dtype() != Dtype::Float16
        {
            return Err("KV update exceeds E4B bounds".into());
        }
        let sliding = layer % 6 != 5;
        let previous = self.layers[layer].take();
        let kv = if sliding {
            if let Some(previous) = previous {
                let begin = (previous.keys.shape()[2] - 511).max(0);
                KV {
                    keys: ops::concatenate(&[previous.keys.index((.., .., begin.., ..)), keys], 2)?,
                    values: ops::concatenate(
                        &[previous.values.index((.., .., begin.., ..)), values],
                        2,
                    )?,
                }
            } else {
                KV { keys, values }
            }
        } else {
            let mut storage = match previous {
                Some(kv) => kv,
                None => KV {
                    keys: Array::zeros::<f32>(&[1, 2, 0, 512])?.as_dtype(Dtype::Float16)?,
                    values: Array::zeros::<f32>(&[1, 2, 0, 512])?.as_dtype(Dtype::Float16)?,
                },
            };
            let capacity = storage.keys.shape()[2];
            if self.offset + count > capacity {
                let growth = ((self.offset + count + 255) / 256) * 256 - capacity;
                let zeros = Array::zeros::<f32>(&[1, 2, growth, 512])?.as_dtype(Dtype::Float16)?;
                storage.keys = ops::concatenate(&[storage.keys, zeros.clone()], 2)?;
                storage.values = ops::concatenate(&[storage.values, zeros], 2)?;
            }
            storage
                .keys
                .index_mut((.., .., self.offset..self.offset + count, ..), keys);
            storage
                .values
                .index_mut((.., .., self.offset..self.offset + count, ..), values);
            storage
        };
        self.layers[layer] = Some(kv);
        self.get(layer, count)
    }

    pub fn get(&self, layer: usize, count: i32) -> Result<KV> {
        if layer >= 24 || !(1..=256).contains(&count) || !(0..=65536 - count).contains(&self.offset)
        {
            return Err("KV read exceeds E4B bounds".into());
        }
        let stored = self.layers[layer]
            .as_ref()
            .ok_or("Missing shared KV source")?;
        let length = if layer % 6 == 5 {
            self.offset + count
        } else {
            stored.keys.shape()[2]
        };
        Ok(KV {
            keys: stored.keys.index((.., .., ..length, ..)),
            values: stored.values.index((.., .., ..length, ..)),
        })
    }

    pub fn evaluate(&self) -> Result<()> {
        self.evaluate_with(&[])
    }

    pub fn evaluate_with(&self, outputs: &[&Array]) -> Result<()> {
        mlx_rs::transforms::eval(
            outputs.iter().copied().chain(
                self.layers
                    .iter()
                    .flatten()
                    .flat_map(|kv| [&kv.keys, &kv.values]),
            ),
        )?;
        Ok(())
    }

    pub fn bytes(&self) -> usize {
        self.layers
            .iter()
            .flatten()
            .map(|kv| kv.keys.nbytes() + kv.values.nbytes())
            .sum()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn retained_prefix_is_unchanged_by_active_cache_extension() {
        let mut active = Cache::default();
        let keys = Array::ones::<f32>(&[1, 2, 3, 512])
            .unwrap()
            .as_dtype(Dtype::Float16)
            .unwrap();
        active.update(5, keys.clone(), keys).unwrap();
        active.offset = 3;
        active.evaluate().unwrap();
        let prefix = active.clone();
        let next = Array::ones::<f32>(&[1, 2, 2, 512])
            .unwrap()
            .multiply(Array::from_f32(2.0))
            .unwrap()
            .as_dtype(Dtype::Float16)
            .unwrap();
        active.update(5, next.clone(), next).unwrap();
        active.offset = 5;
        active.evaluate().unwrap();
        let old = prefix.layers[5]
            .as_ref()
            .unwrap()
            .keys
            .index((.., .., 3..5, ..))
            .as_dtype(Dtype::Float32)
            .unwrap();
        let new = active.layers[5]
            .as_ref()
            .unwrap()
            .keys
            .index((.., .., 3..5, ..))
            .as_dtype(Dtype::Float32)
            .unwrap();
        mlx_rs::transforms::eval([&old, &new]).unwrap();
        assert!(old.as_slice::<f32>().iter().all(|v| *v == 0.0));
        assert!(new.as_slice::<f32>().iter().all(|v| *v == 2.0));
        assert_eq!(prefix.offset, 3);
    }

    #[test]
    fn rejects_precision_drift_and_malformed_cache_shapes() {
        let mut cache = Cache::default();
        for shape in [&[1, 2, 1, 256][..], &[1][..]] {
            let keys = Array::zeros::<f32>(shape).unwrap();
            assert!(cache.update(0, keys.clone(), keys).is_err());
        }
        assert!(cache.get(24, 1).is_err());
        assert_eq!(cache.bytes(), 0);
    }

    #[test]
    fn global_capacity_grows_in_bounded_blocks() {
        let mut cache = Cache::default();
        for count in [1, 256] {
            let keys = Array::ones::<f32>(&[1, 2, count, 512])
                .unwrap()
                .as_dtype(Dtype::Float16)
                .unwrap();
            let kv = cache.update(5, keys.clone(), keys).unwrap();
            assert_eq!(kv.keys.shape()[2], cache.offset + count);
            cache.offset += count;
            cache.evaluate().unwrap();
        }
        assert_eq!(cache.bytes(), 2 * 2 * 512 * 512 * 2);
        cache.offset = 65536;
        let keys = Array::zeros::<f32>(&[1, 2, 1, 512]).unwrap();
        assert!(cache.update(5, keys.clone(), keys).is_err());
    }

    #[test]
    fn sliding_state_retains_only_the_window_and_current_chunk() {
        let mut cache = Cache::default();
        for _ in 0..4 {
            let keys = Array::ones::<f32>(&[1, 2, 256, 256])
                .unwrap()
                .as_dtype(Dtype::Float16)
                .unwrap();
            let kv = cache.update(0, keys.clone(), keys).unwrap();
            assert_eq!(kv.keys.shape()[2], cache.offset.min(511) + 256);
            cache.offset += 256;
            cache.evaluate().unwrap();
        }
        assert_eq!(cache.bytes(), 2 * 2 * 767 * 256 * 2);
    }
}
