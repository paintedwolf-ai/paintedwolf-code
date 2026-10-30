//! Transactional KV cache: staged appends, commit/rollback, and prefix handles bound
//! to retained token state.
//!
//! Slots hold committed KV; a generation stages appends and folds them in only at
//! terminal state, so an interrupted run leaves committed state exactly as it found
//! it. Attention reads the *provisional* view — committed plus staged — so a request
//! always sees its own history. Prefix handles name an exact restorable token state;
//! a handle never outlives the state it names, and a miss always rebuilds.

use std::collections::HashMap;

use crate::error::{Error, Result};

/// One slot's keys and values for each cached position.
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

/// Committed cache state plus one in-flight generation's staging area.
///
/// The cache stores raw slices per slot; the decoder's config owns the topology
/// (which layer reads which slot) and the KV width (`kv_heads * head_dim`). Token
/// counts always divide the full KV width, never one head's width.
pub struct Cache {
    layers: Vec<LayerKv>,
    kv_width: usize,
    committed_len: usize,
    staged: Vec<LayerKv>,
    /// Handle -> the exact token sequence whose KV is committed.
    handles: HashMap<String, Vec<u32>>,
    /// Logits after the last committed token, so a fully cached prompt resumes
    /// without replaying a step.
    last_logits: Option<Vec<f32>>,
}

impl Cache {
    pub fn new(slots: usize, kv_width: usize) -> Self {
        Self {
            layers: vec![LayerKv::new(); slots],
            kv_width,
            committed_len: 0,
            staged: Vec::new(),
            handles: HashMap::new(),
            last_logits: None,
        }
    }

    /// Tokens represented by committed state (full KV width per token).
    pub fn committed_len(&self) -> usize {
        self.committed_len
    }

    /// Tokens represented by committed plus staged state — the position the next
    /// decode step starts at.
    pub fn provisional_len(&self) -> usize {
        let staged = self.staged.first().map(|l| l.k.len() / self.kv_width).unwrap_or(0);
        self.committed_len + staged
    }

    /// Stage one batch of kv for the named slots.
    pub fn stage(&mut self, per_layer: &[(usize, Vec<f32>, Vec<f32>)]) -> Result<()> {
        if self.staged.is_empty() {
            self.staged = vec![LayerKv::new(); self.layers.len()];
        }
        for (layer, k, v) in per_layer {
            if k.len() != v.len() || k.len() % self.kv_width != 0 || *layer >= self.layers.len() {
                return Err(Error::Capacity(format!(
                    "layer {layer}: kv of {} dims does not fit kv_width {}",
                    k.len(),
                    self.kv_width
                )));
            }
            self.staged[*layer].k.extend_from_slice(k);
            self.staged[*layer].v.extend_from_slice(v);
        }
        Ok(())
    }

    /// Fold staged appends into the committed state. Call only on a successful
    /// terminal state; cancels and errors roll back instead.
    pub fn commit(&mut self, logits: Vec<f32>) {
        if self.staged.is_empty() {
            self.last_logits = Some(logits);
            return;
        }
        self.committed_len += self.staged[0].k.len() / self.kv_width;
        let staged = std::mem::take(&mut self.staged);
        for (slot, mut kv) in staged.into_iter().enumerate() {
            self.layers[slot].k.append(&mut kv.k);
            self.layers[slot].v.append(&mut kv.v);
        }
        self.last_logits = Some(logits);
    }

    /// Drop staged appends and any retained logits; committed KV is untouched.
    pub fn rollback(&mut self) {
        self.staged.clear();
        self.last_logits = None;
    }

    /// Register the committed token state under a handle. The handle is reusable
    /// only while that exact token sequence is still what the KV represents.
    pub fn publish(&mut self, handle: &str, tokens: Vec<u32>) {
        if tokens.len() != self.committed_len {
            return; // never publish a handle naming state the KV does not hold
        }
        self.handles.insert(handle.to_string(), tokens);
    }

    /// Resolve a handle to a reusable prefix length: only when the retained token
    /// state is still committed and matches a prefix of the request's prompt.
    pub fn reuse(&self, handle: &Option<String>, prompt: &[u32]) -> Option<usize> {
        let retained = self.handles.get(handle.as_ref()?)?;
        if retained.is_empty() || retained.len() > prompt.len() {
            return None;
        }
        if *retained == prompt[..retained.len()] {
            Some(retained.len())
        } else {
            None
        }
    }

    /// Logits retained from the last commit, for resuming a fully cached prompt.
    pub fn retained_logits(&self) -> Option<&[f32]> {
        self.last_logits.as_deref()
    }

    /// Drop committed state and every handle: nothing survives a full clear.
    pub fn clear_committed(&mut self) {
        self.layers = vec![LayerKv::new(); self.layers.len()];
        self.committed_len = 0;
        self.staged.clear();
        self.handles.clear();
        self.last_logits = None;
    }

    /// Committed KV slices for one slot. Positions index tokens; each token spans
    /// the full KV width and a head reads its `g * head_dim` offset within it.
    pub fn committed_kv(&self, slot: usize) -> (&[f32], &[f32]) {
        (&self.layers[slot].k, &self.layers[slot].v)
    }

    /// Staged KV slices for one slot: the in-flight generation's own tokens.
    pub fn staged_kv(&self, slot: usize) -> (&[f32], &[f32]) {
        match self.staged.get(slot) {
            Some(l) => (&l.k, &l.v),
            None => (&[], &[]),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rollback_leaves_committed_state() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        c.commit(vec![0.0; 3]);
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
        c.commit(vec![0.0; 3]);
        assert_eq!(c.committed_len(), 1);
        assert_eq!(c.committed_kv(1).0, &[5.0, 6.0]);
        assert_eq!(c.provisional_len(), 1);
    }

    #[test]
    fn provisional_counts_staged_tokens() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        assert_eq!(c.provisional_len(), 1);
        c.stage(&[(0, vec![5.0, 6.0], vec![7.0, 8.0])]).unwrap();
        assert_eq!(c.provisional_len(), 2);
        c.rollback();
        assert_eq!(c.provisional_len(), 0);
    }

    #[test]
    fn handles_bind_to_token_state() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0, 1.0, 2.0, 1.0, 2.0], vec![3.0, 4.0, 3.0, 4.0, 3.0, 4.0])]).unwrap();
        c.commit(vec![0.0; 3]);
        c.publish("h", vec![1, 2, 3]);
        // Same tokens: reusable.
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3, 9]), Some(3));
        // A prompt exactly matching the retained state: reusable at full length.
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3]), Some(3));
        // Different prompt: a miss, never a silent reuse.
        assert_eq!(c.reuse(&Some("h".into()), &[7, 8, 9, 9]), None);
        // Unknown handle: a miss.
        assert_eq!(c.reuse(&Some("nope".into()), &[1, 2, 3]), None);
        // No handle: a miss.
        assert_eq!(c.reuse(&None, &[1, 2, 3]), None);
    }

    #[test]
    fn publish_never_names_uncommitted_state() {
        let mut c = Cache::new(4, 2);
        c.publish("h", vec![1, 2, 3]);
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3]), None);
    }

    #[test]
    fn commit_retains_logits() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0], vec![3.0, 4.0])]).unwrap();
        c.commit(vec![0.5, 0.6]);
        assert_eq!(c.retained_logits(), Some(&[0.5, 0.6][..]));
        c.rollback();
        assert_eq!(c.retained_logits(), None);
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
