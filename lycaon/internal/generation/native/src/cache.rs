//! Transactional KV cache with staged appends, commit/rollback, and prefix handles.

use std::collections::HashMap;

use crate::error::{Error, Result};

#[derive(Clone)]
struct LayerKv {
    k: Vec<f32>,
    v: Vec<f32>,
}

impl LayerKv {
    fn new() -> Self {
        Self {
            k: Vec::new(),
            v: Vec::new(),
        }
    }
}

/// Committed KV state plus one active generation's staged appends.
/// Each slot has its own full KV width (`kv_heads * head_dim_for(slot)`), supporting
/// hybrid sliding and full-attention head dimensions.
pub struct Cache {
    layers: Vec<LayerKv>,
    slot_widths: Vec<usize>,
    committed_len: usize,
    staged: Vec<LayerKv>,
    handles: HashMap<String, Vec<u32>>,
    last_logits: Option<Vec<f32>>,
}

impl Cache {
    pub fn new(slots: usize, kv_width: usize) -> Self {
        Self::with_slot_widths(vec![kv_width; slots])
    }

    pub fn with_slot_widths(slot_widths: Vec<usize>) -> Self {
        let slots = slot_widths.len();
        Self {
            layers: vec![LayerKv::new(); slots],
            slot_widths,
            committed_len: 0,
            staged: Vec::new(),
            handles: HashMap::new(),
            last_logits: None,
        }
    }

    pub fn committed_len(&self) -> usize {
        self.committed_len
    }

    pub fn provisional_len(&self) -> usize {
        let staged = match (self.staged.first(), self.slot_widths.first()) {
            (Some(l), Some(&w)) if w > 0 => l.k.len() / w,
            _ => 0,
        };
        self.committed_len + staged
    }

    pub fn slot_width(&self, slot: usize) -> usize {
        self.slot_widths.get(slot).copied().unwrap_or(0)
    }

    pub fn stage(&mut self, per_layer: &[(usize, Vec<f32>, Vec<f32>)]) -> Result<()> {
        if self.staged.is_empty() {
            self.staged = vec![LayerKv::new(); self.layers.len()];
        }
        for (layer, k, v) in per_layer {
            let Some(&width) = self.slot_widths.get(*layer) else {
                return Err(Error::Capacity(format!(
                    "layer {layer} exceeds {} cache slots",
                    self.layers.len()
                )));
            };
            if width == 0 || k.len() != v.len() || k.len() % width != 0 {
                return Err(Error::Capacity(format!(
                    "layer {layer}: kv of {} dims does not fit slot width {width}",
                    k.len()
                )));
            }
            self.staged[*layer].k.extend_from_slice(k);
            self.staged[*layer].v.extend_from_slice(v);
        }
        Ok(())
    }

    pub fn commit(&mut self, logits: Vec<f32>) {
        if self.staged.is_empty() {
            self.last_logits = Some(logits);
            return;
        }
        if let Some(&w) = self.slot_widths.first()
            && w > 0
        {
            self.committed_len += self.staged[0].k.len() / w;
        }
        let staged = std::mem::take(&mut self.staged);
        for (slot, mut kv) in staged.into_iter().enumerate() {
            self.layers[slot].k.append(&mut kv.k);
            self.layers[slot].v.append(&mut kv.v);
        }
        self.last_logits = Some(logits);
    }

    pub fn rollback(&mut self) {
        self.staged.clear();
        self.last_logits = None;
    }

    pub fn publish(&mut self, handle: &str, tokens: Vec<u32>) {
        if tokens.len() != self.committed_len {
            return;
        }
        self.handles.insert(handle.to_string(), tokens);
    }

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

    pub fn retained_logits(&self) -> Option<&[f32]> {
        self.last_logits.as_deref()
    }

    pub fn clear_committed(&mut self) {
        self.layers = vec![LayerKv::new(); self.layers.len()];
        self.committed_len = 0;
        self.staged.clear();
        self.handles.clear();
        self.last_logits = None;
    }

    pub fn committed_kv(&self, slot: usize) -> (&[f32], &[f32]) {
        (&self.layers[slot].k, &self.layers[slot].v)
    }

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
    fn hybrid_slot_widths_validate_per_layer() {
        let mut c = Cache::with_slot_widths(vec![4, 8]);
        c.stage(&[
            (0, vec![1.0; 4], vec![2.0; 4]),
            (1, vec![3.0; 8], vec![4.0; 8]),
        ])
        .unwrap();
        c.commit(vec![0.0; 2]);
        assert_eq!(c.committed_len(), 1);
        assert!(c.stage(&[(1, vec![1.0; 4], vec![2.0; 4])]).is_err());
    }

    #[test]
    fn handles_bind_to_token_state() {
        let mut c = Cache::new(4, 2);
        c.stage(&[(0, vec![1.0, 2.0, 1.0, 2.0, 1.0, 2.0], vec![3.0, 4.0, 3.0, 4.0, 3.0, 4.0])])
            .unwrap();
        c.commit(vec![0.0; 3]);
        c.publish("h", vec![1, 2, 3]);
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3, 9]), Some(3));
        assert_eq!(c.reuse(&Some("h".into()), &[1, 2, 3]), Some(3));
        assert_eq!(c.reuse(&Some("h".into()), &[7, 8, 9, 9]), None);
        assert_eq!(c.reuse(&Some("nope".into()), &[1, 2, 3]), None);
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
