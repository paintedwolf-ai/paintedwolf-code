//! Transactional KV cache: staged appends, commit/rollback, and prefix handles bound
//! to retained token state.
//!
//! `source_layers` slots hold committed KV. Layers beyond the source reuse the last
//! source slot of their kind (sliding or full). A generation stages appends and folds
//! them in only at terminal state, so an interrupted run leaves committed state exactly
//! as it found it. Prefix handles name a retained token prefix; a handle never outlives
//! the state it names, and a miss always rebuilds from scratch.

use std::collections::HashMap;

use crate::error::{Error, Result};

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
}

/// Maps a logical layer index to its owned cache slot and sliding/full kind.
pub trait LayerMap: Fn(usize) -> (usize, bool) {}
impl<T: Fn(usize) -> (usize, bool)> LayerMap for T {}

/// Committed cache state plus one in-flight generation's staging area.
///
/// `slot_for` returns `(cache_slot, is_sliding)` for a logical layer index; the
/// decoder's config owns the topology, the cache only stores what it is told.
pub struct Cache {
    layers: Vec<LayerKv>,
    head_dim: usize,
    committed_len: usize,
    staged: Vec<LayerKv>,
    /// Handle -> retained token prefix (tokens, not just a length).
    handles: HashMap<String, Vec<u32>>,
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

    pub fn committed_len(&self) -> usize {
        self.committed_len
    }

    /// Stage one batch of kv for the named slots.
    pub fn stage(&mut self, per_layer: &[(usize, Vec<f32>, Vec<f32>)]) -> Result<()> {
        if self.staged.is_empty() {
            self.staged = vec![LayerKv::new(); self.layers.len()];
        }
        for (layer, k, v) in per_layer {
            if k.len() != v.len() || k.len() % self.head_dim != 0 || layer >= &self.layers.len() {
                return Err(Error::Capacity(format!(
                    "layer {layer}: kv of {} dims does not fit head_dim {}",
                    k.len(),
                    self.head_dim
                )));
            }
            self.staged[*layer].k.extend_from_slice(k);
            self.staged[*layer].v.extend_from_slice(v);
        }
        Ok(())
    }

    /// Fold staged appends into the committed state. Call only at terminal state.
    pub fn commit(&mut self) {
        if self.staged.is_empty() {
            return;
        }
        self.committed_len += self.staged[0].k.len() / self.head_dim;
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

    /// Register the current committed token prefix under a handle. The handle is only
    /// reusable while that exact token state is still committed.
    pub fn publish(&mut self, handle: &str, prefix_tokens: Vec<u32>) {
        self.handles.insert(handle.to_string(), prefix_tokens);
    }

    /// Resolve a handle to a reusable prefix length: the retained token state's length,
    /// only when it matches a prefix of the request's prompt. A miss returns None and
    /// the caller rebuilds from scratch.
    pub fn reuse(&self, handle: &Option<String>, prompt: &[u32]) -> Option<usize> {
        let retained = self.handles.get(handle.as_ref()?)?;
        let prompt_prefix: Vec<u32> = prompt.iter().take(retained.len()).copied().collect();
        if *retained == prompt_prefix && !retained.is_empty() {
            Some(retained.len())
        } else {
            None
        }
    }

    /// Drop committed state and every handle: nothing survives a full clear.
    pub fn clear_committed(&mut self) {
        self.layers = vec![LayerKv::new(); self.layers.len()];
        self.committed_len = 0;
        self.staged.clear();
        self.handles.clear();
    }

    pub fn committed_kv(&self, slot: usize) -> (&[f32], &[f32]) {
        (&self.layers[slot].k, &self.layers[slot].v)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rollback_leaves_committed_state() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        c.commit();
        let committed = c.committed_kv(0).0.to_vec();

        c.stage(&[(0, vec![9.0, 9.0], vec![9.0, 9.0])]).unwrap();
        c.rollback();
        assert_eq!(c.committed_kv(0).0, &committed[..]);
        assert_eq!(c.committed_len(), 1);
    }

    #[test]
    fn commit_folds_and_advances_len() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        c.stage(&[(1, vec![5.0, 6.0], vec![7.0, 8.0])]).unwrap();
        c.commit();
        assert_eq!(c.committed_len(), 1);
        assert_eq!(c.committed_kv(1).0, &[5.0, 6.0]);
    }

    #[test]
    fn handles_bind_to_token_state() {
        let mut c = Cache::new(4, 2);
        c.publish("h", vec![1, 2, 3]);
        // Same tokens: reusable.
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3, 9]), Some(3));
        // Different prompt: a miss, never a silent reuse.
        assert_eq!(c.reuse(&Some("h".into()), &[7, 8, 9, 9]), None);
        // Unknown handle: a miss.
        assert_eq!(c.reuse(&Some("nope".into()), &[1, 2, 3]), None);
        // No handle: a miss.
        assert_eq!(c.reuse(&None, &[1, 2, 3]), None);
    }

    #[test]
    fn clear_drops_handles() {
        let mut c = Cache::new(4, 2);
        c.publish("h", vec![1, 2, 3]);
        c.clear_committed();
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3]), None);
    }

    #[test]
    fn staging_rejects_bad_width() {
        let mut c = Cache::new(4, 2);
        assert!(c.stage(&[(0, vec![1.0, 2.0, 3.0], vec![1.0, 2.0, 3.0])]).is_err());
        assert!(c.stage(&[(99, vec![1.0, 2.0], vec![1.0, 2.0])]).is_err());
    }
}
