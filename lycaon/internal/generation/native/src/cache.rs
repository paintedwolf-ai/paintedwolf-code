//! Transactional KV cache: staged appends with commit, rollback and prefix reuse.
//!
//! Layers 0..23 own cache slots; sliding layers past 22 reuse source layer 22 and full
//! layers past 23 reuse source layer 23, per the Gemma 4 hybrid layout. A generation
//! stages appends and commits only when the request reaches a terminal state, so a
//! canceled or failed run leaves the cache exactly as it found it.

use std::collections::HashMap;

use crate::error::{Error, Result};

pub const SOURCE_LAYERS: usize = 24;
const LAST_SLIDING: usize = 22;
const LAST_FULL: usize = 23;

/// One layer's keys and values for each cached position.
#[derive(Clone)]
struct LayerKv {
    k: Vec<f32>,
    v: Vec<f32>,
}

impl LayerKv {
    fn new() -> Self {
        Self { k: Vec::new(), v: Vec::new() }
    }
    fn len(&self) -> usize {
        self.k.len()
    }
}

/// Committed cache state plus one in-flight generation's staging area.
pub struct Cache {
    layers: Vec<LayerKv>,
    head_dim: usize,
    committed_len: usize,
    staged: Vec<LayerKv>,
    handles: HashMap<String, usize>,
}

impl Cache {
    pub fn new(source_layers: usize, head_dim: usize) -> Self {
        Self {
            layers: vec![LayerKv::new(); source_layers],
            head_dim,
            committed_len: 0,
            staged: Vec::new(),
            handles: HashMap::new(),
        }
    }

    /// Slot for a source layer; layers beyond the source reuse the last sliding/full slot.
    fn slot(&self, layer: usize) -> usize {
        let last_owned = SOURCE_LAYERS - 1;
        if layer < SOURCE_LAYERS {
            layer
        } else if (layer - SOURCE_LAYERS) % 2 == 0 {
            LAST_SLIDING.min(last_owned)
        } else {
            LAST_FULL.min(last_owned)
        }
    }

    pub fn committed_len(&self) -> usize {
        self.committed_len
    }

    /// Stage one position's kv for every source layer.
    pub fn stage(&mut self, per_layer: &[(usize, Vec<f32>, Vec<f32>)]) -> Result<()> {
        if self.staged.is_empty() {
            self.staged = vec![LayerKv::new(); self.layers.len()];
        }
        for (layer, k, v) in per_layer {
            if k.len() != self.head_dim || v.len() != self.head_dim {
                return Err(Error::Capacity(format!(
                    "layer {layer}: kv of {} dims does not match head_dim {}",
                    k.len(),
                    self.head_dim
                )));
            }
            let slot = self.slot(*layer);
            self.staged[slot].k.extend_from_slice(k);
            self.staged[slot].v.extend_from_slice(v);
        }
        Ok(())
    }

    /// Fold staged appends into the committed state.
    pub fn commit(&mut self) {
        if self.staged.is_empty() {
            return;
        }
        self.committed_len += self.staged[0].len() / self.head_dim;
        let staged = std::mem::take(&mut self.staged);
        for (slot, mut kv) in staged.into_iter().enumerate() {
            self.layers[slot].k.append(&mut kv.k);
            self.layers[slot].v.append(&mut kv.v);
        }
    }

    /// Drop staged appends; committed state is untouched.
    pub fn rollback(&mut self) {
        self.staged.clear();
    }

    /// Register the committed prefix under a host-provided handle.
    pub fn publish(&mut self, handle: &str) {
        self.handles.insert(handle.to_string(), self.committed_len);
    }

    /// Prefix length a handle refers to, if still resident.
    pub fn reuse(&self, handle: &Option<String>) -> Option<usize> {
        let h = handle.as_ref()?;
        self.handles.get(h).copied()
    }

    /// Drop handles whose prefix no longer matches committed state.
    pub fn evict_stale(&mut self) {
        self.handles.retain(|_, l| *l <= self.committed_len);
    }

    pub fn committed_kv(&self, layer: usize) -> (&[f32], &[f32]) {
        let slot = self.slot(layer);
        (&self.layers[slot].k, &self.layers[slot].v)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rollback_leaves_committed_state() {
        let mut c = Cache::new(SOURCE_LAYERS, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        c.commit();
        let committed = c.committed_kv(0).0.to_vec();

        c.stage(&[(0, vec![9.0, 9.0], vec![9.0, 9.0])]).unwrap();
        c.rollback();
        assert_eq!(c.committed_kv(0).0, &committed[..]);
        assert_eq!(c.committed_len(), 1);
    }

    #[test]
    fn layers_beyond_source_share_slots() {
        let c = Cache::new(SOURCE_LAYERS, 2);
        assert_eq!(c.slot(0), 0);
        assert_eq!(c.slot(23), 23);
        assert_eq!(c.slot(24), LAST_SLIDING);
        assert_eq!(c.slot(25), LAST_FULL);
    }

    #[test]
    fn handles_survive_until_prefix_moves() {
        let mut c = Cache::new(SOURCE_LAYERS, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        c.commit();
        c.publish("p1");
        assert_eq!(c.reuse(&Some("p1".into())), Some(1));
        c.evict_stale();
        assert_eq!(c.reuse(&Some("p1".into())), Some(1));
    }
}
