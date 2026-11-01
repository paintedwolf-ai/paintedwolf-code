use super::Result;
use mlx_rs::{Array, Dtype, ops, ops::indexing::*};
use std::{cell::RefCell, rc::Rc};

#[derive(Clone)]
pub struct KV {
    pub keys: Array,
    pub values: Array,
}

#[derive(Clone)]
pub struct Cache {
    pub offset: i32,
    layers: Vec<Option<KV>>,
    globals: [Option<Rc<RefCell<KV>>>; 4],
    capacity: i32,
}

impl Cache {
    pub fn new(capacity: usize) -> Result<Self> {
        if !(2..=65536).contains(&capacity) {
            return Err("KV capacity exceeds E4B bounds".into());
        }
        Ok(Self {
            offset: 0,
            layers: (0..24).map(|_| None).collect(),
            globals: std::array::from_fn(|_| None),
            capacity: capacity as i32,
        })
    }

    pub fn clear(&mut self) {
        self.offset = 0;
        self.layers.fill(None);
        self.globals.fill(None);
    }

    pub fn update(&mut self, layer: usize, keys: Array, values: Array) -> Result<KV> {
        let shape = keys.shape();
        let count = shape.get(2).copied().unwrap_or(0);
        let dimension = if layer % 6 == 5 { 512 } else { 256 };
        if layer >= 24
            || !(1..=256).contains(&count)
            || !(0..=self.capacity - count).contains(&self.offset)
            || shape != [1, 2, count, dimension]
            || values.shape() != shape
            || keys.dtype() != Dtype::Float16
            || values.dtype() != Dtype::Float16
        {
            return Err("KV update exceeds E4B bounds".into());
        }
        let sliding = layer % 6 != 5;
        if sliding {
            let kv = if let Some(previous) = self.layers[layer].take() {
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
            };
            self.layers[layer] = Some(kv);
        } else {
            let bank = match self.globals[layer / 6].take() {
                Some(bank) => bank,
                None => Rc::new(RefCell::new(KV {
                    keys: Array::zeros::<half::f16>(&[1, 2, self.capacity, 512])?,
                    values: Array::zeros::<half::f16>(&[1, 2, self.capacity, 512])?,
                })),
            };
            let mut storage = bank.borrow_mut();
            // Appends leave the retained prefix range unchanged.
            storage
                .keys
                .index_mut((.., .., self.offset..self.offset + count, ..), keys);
            storage
                .values
                .index_mut((.., .., self.offset..self.offset + count, ..), values);
            drop(storage);
            self.globals[layer / 6] = Some(bank);
        }
        self.get(layer, count)
    }

    pub fn get(&self, layer: usize, count: i32) -> Result<KV> {
        if layer >= 24
            || !(1..=256).contains(&count)
            || !(0..=self.capacity - count).contains(&self.offset)
        {
            return Err("KV read exceeds E4B bounds".into());
        }
        let stored = if layer % 6 == 5 {
            self.globals[layer / 6]
                .as_ref()
                .ok_or("Missing global KV source")?
                .borrow()
                .clone()
        } else {
            self.layers[layer]
                .as_ref()
                .ok_or("Missing sliding KV source")?
                .clone()
        };
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
        let globals: Vec<Array> = self
            .globals
            .iter()
            .flatten()
            .flat_map(|bank| {
                let kv = bank.borrow();
                [kv.keys.clone(), kv.values.clone()]
            })
            .collect();
        mlx_rs::transforms::eval(
            outputs.iter().copied().chain(globals.iter()).chain(
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
            .sum::<usize>()
            + self
                .globals
                .iter()
                .flatten()
                .map(|bank| {
                    let kv = bank.borrow();
                    kv.keys.nbytes() + kv.values.nbytes()
                })
                .sum::<usize>()
    }

    pub fn resident_bytes_with(&self, retained: &Self) -> usize {
        let shared = self
            .globals
            .iter()
            .zip(&retained.globals)
            .filter_map(|(active, saved)| {
                let (active, saved) = (active.as_ref()?, saved.as_ref()?);
                Rc::ptr_eq(active, saved).then(|| {
                    let kv = active.borrow();
                    kv.keys.nbytes() + kv.values.nbytes()
                })
            })
            .sum::<usize>();
        self.bytes() + retained.bytes() - shared
    }
}

#[cfg(test)]
#[path = "cache_tests.rs"]
mod tests;
