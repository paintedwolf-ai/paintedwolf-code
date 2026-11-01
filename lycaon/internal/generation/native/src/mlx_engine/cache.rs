use super::Result;
use mlx_rs::{Array, Dtype, ops, ops::indexing::*};

pub struct KV {
    pub keys: Array,
    pub values: Array,
}

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
        let count = keys.shape()[2];
        if layer >= 24 || count == 0 || count > 256 || self.offset + count > 65536 {
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
        mlx_rs::transforms::eval(
            self.layers
                .iter()
                .flatten()
                .flat_map(|kv| [&kv.keys, &kv.values]),
        )?;
        Ok(())
    }

    pub fn bytes(&self) -> usize {
        self.layers
            .iter()
            .flatten()
            .map(|kv| (kv.keys.size() + kv.values.size()) * 2)
            .sum()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
