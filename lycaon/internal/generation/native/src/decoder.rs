//! The Gemma 4 E4B decoder on CPU, kept exact so accelerated backends have a reference.
//!
//! Topology follows the E4B hybrid layout: `source_layers` (24) own KV slots, and the
//! remaining shared layers read the last source slot of their kind — sliding layers
//! read the last sliding slot, full layers the last full slot. Sliding and full
//! attention use different head dimensions and RoPE policies (sliding applies RoPE,
//! full does not). Q/K carry learned RMSNorm, V carries an unweighted RMS
//! normalization, attention scale stays 1.0, MLPs are GeGLU (gelu-gated), and final
//! logits pass through a tanh softcap. PLE per-layer embeddings add into the token
//! embedding at their position when a checkpoint carries the table.

use std::collections::HashMap;

use crate::error::{Error, Result};

pub const LOGIT_SOFTCAP: f32 = 30.0;
pub const DEFAULT_SLIDING_WINDOW: usize = 512;
/// E4B layout: 24 layers own cache slots; the rest are shared.
pub const E4B_SOURCE_LAYERS: usize = 24;
/// E4B layout: 18 of the shared layers reuse their source slot.
pub const E4B_SHARED_LAYERS: usize = 18;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LayerKind {
    Sliding,
    Full,
}

#[derive(Clone)]
pub struct Config {
    pub hidden: usize,
    pub heads: usize,
    pub kv_heads: usize,
    /// Head dimension for sliding layers.
    pub head_dim: usize,
    /// Head dimension for full-attention layers (E4B uses a larger dim with no RoPE).
    pub full_head_dim: usize,
    /// Layers that own cache slots (and K/V weights).
    pub source_layers: usize,
    pub total_layers: usize,
    pub rope_theta: f32,
    pub vocab: usize,
    pub eps: f32,
    pub sliding_window: usize,
    /// Whether full-attention layers skip RoPE (the E4B contract says they do).
    pub full_layers_skip_rope: bool,
    /// Attention scale. The E4B contract fixes it at 1.0; Q/K RMSNorm already
    /// normalizes the query.
    pub attention_scale: f32,
    /// Whether V carries an RMS normalization (without learned weights).
    pub normalize_v: bool,
    /// Number of per-layer embeddings (PLE) after the embedding table, if any.
    pub ple_layers: usize,
}

impl Config {
    pub fn from_json(v: &serde_json::Value) -> Result<Self> {
        let g = |k: &str| -> Result<usize> {
            v.get(k).and_then(|x| x.as_u64()).map(|x| x as usize)
                .ok_or_else(|| Error::Checkpoint(format!("config: no {k}")))
        };
        let f = |k: &str| -> Result<f32> {
            v.get(k).and_then(|x| x.as_f64()).map(|x| x as f32)
                .ok_or_else(|| Error::Checkpoint(format!("config: no {k}")))
        };
        let hidden = g("hidden_size")?;
        let heads = g("num_attention_heads")?;
        let kv_heads = g("num_key_value_heads")?;
        let head_dim = v.get("head_dim").and_then(|x| x.as_u64())
            .map(|x| x as usize).unwrap_or(hidden / heads);
        // Sliding and full attention may use different head dimensions; the E4B
        // layout gives full attention a larger dimension and skips RoPE there.
        let full_head_dim = v.get("full_head_dim").and_then(|x| x.as_u64())
            .map(|x| x as usize).unwrap_or(head_dim);
        let total = g("num_hidden_layers")?;
        // The E4B contract: `source_layers` own cache slots, shared layers count is
        // explicit and distinct. Accept either spelling; never conflate the two.
        let source = match v.get("num_kv_shared_layers") {
            Some(x) if x.is_u64() => {
                let shared = x.as_u64().unwrap() as usize;
                total.checked_sub(shared).ok_or_else(|| {
                    Error::Checkpoint(format!("config: {shared} shared layers exceed {total}"))
                })?
            }
            _ => v.get("num_source_layers").and_then(|x| x.as_u64())
                .map(|x| x as usize).unwrap_or(total),
        };
        let ple = v.get("num_ple_layers").and_then(|x| x.as_u64())
            .map(|x| x as usize).unwrap_or(0);
        if heads % kv_heads != 0 {
            return Err(Error::Checkpoint(format!(
                "config: {heads} query heads do not group into {kv_heads} kv heads"
            )));
        }
        Ok(Self {
            hidden,
            heads,
            kv_heads,
            head_dim,
            full_head_dim,
            source_layers: source,
            total_layers: total,
            rope_theta: f("rope_theta")?,
            vocab: g("vocab_size")?,
            eps: f("rms_norm_eps")?,
            sliding_window: v.get("sliding_window").and_then(|x| x.as_u64())
                .map(|x| x as usize).unwrap_or(DEFAULT_SLIDING_WINDOW),
            full_layers_skip_rope: v.get("full_layers_skip_rope").and_then(|x| x.as_bool()).unwrap_or(true),
            attention_scale: v.get("attention_scale").and_then(|x| x.as_f64())
                .map(|x| x as f32).unwrap_or(1.0),
            normalize_v: v.get("normalize_v").and_then(|x| x.as_bool()).unwrap_or(true),
            ple_layers: ple,
        })
    }

    fn head_dim_for(&self, layer: usize) -> usize {
        if self.layer_kind(layer) == LayerKind::Full { self.full_head_dim } else { self.head_dim }
    }

    fn rope_applies(&self, layer: usize) -> bool {
        !(self.full_layers_skip_rope && self.layer_kind(layer) == LayerKind::Full)
    }

    /// Hybrid pattern: every 6th layer is full attention, the rest sliding.
    pub fn layer_kind(&self, layer: usize) -> LayerKind {
        if layer % 6 == 5 { LayerKind::Full } else { LayerKind::Sliding }
    }

    /// Cache slot a layer reads and writes: owned layers use their own index; shared
    /// layers reuse the last source slot of their kind.
    pub fn cache_slot(&self, layer: usize) -> usize {
        if layer < self.source_layers {
            return layer;
        }
        let kind = self.layer_kind(layer);
        (0..self.source_layers)
            .rev()
            .find(|&i| self.layer_kind(i) == kind)
            .unwrap_or(self.source_layers - 1)
    }

    fn q_width(&self, layer: usize) -> usize { self.heads * self.head_dim_for(layer) }
    fn kv_width(&self, layer: usize) -> usize { self.kv_heads * self.head_dim_for(layer) }
}

pub struct Weights {
    pub tensors: HashMap<String, Vec<f32>>,
}

impl Weights {
    fn take(&mut self, name: &str) -> Result<Vec<f32>> {
        self.tensors
            .remove(name)
            .ok_or_else(|| Error::Checkpoint(format!("no tensor {name}")))
    }
}

pub struct Layer {
    pub pre_attn_norm: Vec<f32>,
    pub post_attn_norm: Option<Vec<f32>>,
    pub q_proj: Vec<f32>,
    /// Only source layers carry K/V weights; shared layers reuse their source slot's cache.
    pub k_proj: Option<Vec<f32>>,
    pub v_proj: Option<Vec<f32>>,
    pub o_proj: Vec<f32>,
    /// Q norm has one weight per head's dim; K norm likewise, one per KV head.
    pub q_norm: Vec<f32>,
    pub k_norm: Vec<f32>,
    pub pre_mlp_norm: Vec<f32>,
    pub post_mlp_norm: Option<Vec<f32>>,
    pub gate_proj: Vec<f32>,
    pub up_proj: Vec<f32>,
    pub down_proj: Vec<f32>,
}

pub struct Decoder {
    pub cfg: Config,
    pub embed: Vec<f32>,
    /// Per-layer embeddings (PLE), one table per configured layer.
    pub ple: Vec<Vec<f32>>,
    pub layers: Vec<Layer>,
    pub final_norm: Vec<f32>,
}

impl Decoder {
    pub fn load(cfg: Config, mut w: Weights) -> Result<Self> {
        let mut layers = Vec::with_capacity(cfg.total_layers);
        for i in 0..cfg.total_layers {
            let p = format!("model.layers.{i}");
            let owned = i < cfg.source_layers;
            let has_block_norms = w.tensors.contains_key(&format!("{p}.post_attention_layernorm.weight"));
            layers.push(Layer {
                pre_attn_norm: w.take(&format!("{p}.input_layernorm.weight"))?,
                post_attn_norm: if has_block_norms {
                    Some(w.take(&format!("{p}.post_attention_layernorm.weight"))?)
                } else { None },
                q_proj: w.take(&format!("{p}.self_attn.q_proj.weight"))?,
                k_proj: if owned {
                    Some(w.take(&format!("{p}.self_attn.k_proj.weight"))?)
                } else { None },
                v_proj: if owned {
                    Some(w.take(&format!("{p}.self_attn.v_proj.weight"))?)
                } else { None },
                o_proj: w.take(&format!("{p}.self_attn.o_proj.weight"))?,
                q_norm: w.take(&format!("{p}.self_attn.q_norm.weight"))?,
                k_norm: w.take(&format!("{p}.self_attn.k_norm.weight"))?,
                pre_mlp_norm: w.take(&format!("{p}.pre_feedforward_layernorm.weight"))?,
                post_mlp_norm: if has_block_norms {
                    Some(w.take(&format!("{p}.post_feedforward_layernorm.weight"))?)
                } else { None },
                gate_proj: w.take(&format!("{p}.mlp.gate_proj.weight"))?,
                up_proj: w.take(&format!("{p}.mlp.up_proj.weight"))?,
                down_proj: w.take(&format!("{p}.mlp.down_proj.weight"))?,
            });
        }
        let ple = (0..cfg.ple_layers)
            .map(|i| w.take(&format!("model.ple.{i}.weight")))
            .collect::<Result<Vec<_>>>()?;
        Ok(Self {
            embed: w.take("model.embed_tokens.weight")?,
            ple,
            layers,
            final_norm: w.take("model.norm.weight")?,
            cfg,
        })
    }

    fn embed_one(&self, t: u32) -> Result<Vec<f32>> {
        let c = &self.cfg;
        let off = t as usize * c.hidden;
        self.embed.get(off..off + c.hidden).map(<[f32]>::to_vec)
            .ok_or_else(|| Error::Checkpoint(format!("token {t} out of embedding range")))
    }

    /// One decode step over the current sequence. Returns logits for the last position
    /// plus per-source-layer kv to stage. Attention reads the provisional cache view
    /// (committed plus staged) so every token sees its full history.
    pub fn step(
        &self,
        tokens: &[u32],
        kv: &crate::cache::Cache,
        next_pos: usize,
    ) -> Result<(Vec<f32>, Vec<(usize, Vec<f32>, Vec<f32>)>)> {
        let c = &self.cfg;
        if tokens.is_empty() {
            return Err(Error::Capacity("empty sequence".into()));
        }
        let mut x: Vec<Vec<f32>> = tokens.iter().map(|&t| self.embed_one(t)).collect::<Result<_>>()?;

        // PLE adds a learned per-position vector to the token embedding, layer-scoped.
        for (s, row) in x.iter_mut().enumerate() {
            let pos = next_pos + s;
            for ple in &self.ple {
                if pos < ple.len() / c.hidden {
                    let off = pos * c.hidden;
                    for (r, p) in row.iter_mut().zip(&ple[off..off + c.hidden]) {
                        *r += p;
                    }
                }
            }
        }

        let mut staged: Vec<(usize, Vec<f32>, Vec<f32>)> = Vec::new();
        for (i, layer) in self.layers.iter().enumerate() {
            let kv_slot = c.cache_slot(i);
            let normed = rms_norm(&x, &layer.pre_attn_norm, c.eps);
            let attn = self.attention(&normed, kv, kv_slot, next_pos, i, &mut staged)?;
            let mut h = if let Some(pn) = &layer.post_attn_norm {
                let n = rms_norm(&attn, pn, c.eps);
                add_rows(&x, &n)
            } else {
                add_rows(&x, &attn)
            };
            let m = rms_norm(&h, &layer.pre_mlp_norm, c.eps);
            let mlp = self.mlp(layer, &m);
            h = match &layer.post_mlp_norm {
                Some(pn) => {
                    let n = rms_norm(&mlp, pn, c.eps);
                    add_rows(&h, &n)
                }
                None => add_rows(&h, &mlp),
            };
            x = h;
        }

        let n = rms_norm(&x, &self.final_norm, c.eps);
        let last = n.last().ok_or_else(|| Error::Capacity("empty hidden state".into()))?;
        let logits: Vec<f32> = (0..c.vocab)
            .map(|v| {
                let off = v * c.hidden;
                let dot: f32 = self.embed
                    .get(off..off + c.hidden)
                    .map(|row| row.iter().zip(last).map(|(a, b)| a * b).sum())
                    .unwrap_or(0.0);
                softcap(dot, LOGIT_SOFTCAP)
            })
            .collect();
        Ok((logits, staged))
    }

    #[allow(clippy::too_many_arguments)]
    fn attention(
        &self,
        h: &[Vec<f32>],
        kv: &crate::cache::Cache,
        kv_slot: usize,
        next_pos: usize,
        layer_idx: usize,
        staged: &mut Vec<(usize, Vec<f32>, Vec<f32>)>,
    ) -> Result<Vec<Vec<f32>>> {
        let c = &self.cfg;
        let layer = &self.layers[layer_idx];
        let seq = h.len();
        let dim = c.head_dim_for(layer_idx);
        let q_w = c.q_width(layer_idx);
        let kv_w = c.kv_width(layer_idx);

        let q: Vec<Vec<f32>> = h.iter().map(|t| matvec(&layer.q_proj, t, q_w)).collect();
        // Shared layers have no K/V weights: their cache slot was filled by the source layer.
        let owned_kv = layer.k_proj.is_some();
        let (new_k, new_v): (Vec<f32>, Vec<f32>) = if owned_kv {
            let k: Vec<Vec<f32>> = h.iter().map(|t| matvec(layer.k_proj.as_ref().unwrap(), t, kv_w)).collect();
            let v: Vec<Vec<f32>> = h.iter().map(|t| matvec(layer.v_proj.as_ref().unwrap(), t, kv_w)).collect();
            let mut nk = Vec::with_capacity(seq * kv_w);
            let mut nv = Vec::with_capacity(seq * kv_w);
            for (kt, vt) in k.iter().zip(&v) {
                // Each KV head is normalized separately against its own dim slice.
                for g in 0..c.kv_heads {
                    let head = &kt[g * dim..(g + 1) * dim];
                    let w_slice = &layer.k_norm[g * dim..(g + 1) * dim];
                    nk.extend_from_slice(&rms_vec(head, w_slice, c.eps));
                }
                // V normalization is RMS without learned weights.
                if c.normalize_v {
                    for g in 0..c.kv_heads {
                        let head = &vt[g * dim..(g + 1) * dim];
                        nv.extend_from_slice(&rms_vec(head, &vec![1.0; dim], c.eps));
                    }
                } else {
                    nv.extend_from_slice(vt);
                }
            }
            (nk, nv)
        } else {
            (Vec::new(), Vec::new())
        };
        if owned_kv {
            staged.push((kv_slot, new_k.clone(), new_v.clone()));
        }

        let heads_per_group = c.heads / c.kv_heads;
        let scale = c.attention_scale;
        let applies_rope = c.rope_applies(layer_idx);
        let mut out = Vec::with_capacity(seq);
        for (s, qs) in q.iter().enumerate() {
            let pos = next_pos + s;
            let mut acc = vec![0f32; q_w];
            for g in 0..c.kv_heads {
                for qi in 0..heads_per_group {
                    let qh = g * heads_per_group + qi;
                    let qv = rms_vec(&qs[qh * dim..(qh + 1) * dim], &layer.q_norm, c.eps);
                    let qv = if applies_rope { rope(&qv, pos, c.rope_theta) } else { qv };
                    // Collect scores over allowed positions, then softmax.
                    let mut scores: Vec<f32> = Vec::new();
                    let mut vs: Vec<Vec<f32>> = Vec::new();
                    // Committed history: full KV width per token; this head reads its
                    // own slice within each token.
                    let (ck, cv) = kv.committed_kv(kv_slot);
                    let committed_tokens = ck.len() / kv_w;
                    let is_sliding = c.layer_kind(layer_idx) == LayerKind::Sliding;
                    for p in 0..committed_tokens {
                        if masked(c, is_sliding, pos, p) {
                            continue;
                        }
                        let off = p * kv_w + g * dim;
                        let kp = if applies_rope {
                            rope(&ck[off..off + dim].to_vec(), p, c.rope_theta)
                        } else {
                            ck[off..off + dim].to_vec()
                        };
                        let dot: f32 = qv.iter().zip(&kp).map(|(a, b)| a * b).sum::<f32>() * scale;
                        scores.push(dot);
                        vs.push(cv[off..off + dim].to_vec());
                    }
                    // This request's staged tokens, including earlier ones in the same
                    // prefill batch — provisional state, not just committed.
                    if owned_kv {
                        let (sk, sv) = kv.staged_kv(kv_slot);
                        let staged_tokens = sk.len() / kv_w;
                        for p2 in 0..staged_tokens {
                            let p = next_pos + p2;
                            if p > pos {
                                continue; // causal within the batch
                            }
                            if masked(c, is_sliding, pos, p) {
                                continue;
                            }
                            let off = p2 * kv_w + g * dim;
                            let kp = if applies_rope {
                                rope(&sk[off..off + dim].to_vec(), p, c.rope_theta)
                            } else {
                                sk[off..off + dim].to_vec()
                            };
                            let dot: f32 = qv.iter().zip(&kp).map(|(a, b)| a * b).sum::<f32>() * scale;
                            scores.push(dot);
                            vs.push(sv[off..off + dim].to_vec());
                        }
                    }
                    let weights = softmax(&scores);
                    let mut best = vec![0f32; dim];
                    for (vrow, wp) in vs.iter().zip(&weights) {
                        for (bi, bv) in best.iter_mut().zip(vrow) {
                            *bi += wp * bv;
                        }
                    }
                    acc[qh * dim..(qh + 1) * dim].copy_from_slice(&best);
                }
            }
            // heads layout is [head, dim]; o_proj expects the flat hidden row in that order.
            out.push(matvec(&layer.o_proj, &acc, c.hidden));
        }
        Ok(out)
    }

    fn mlp(&self, layer: &Layer, x: &[Vec<f32>]) -> Vec<Vec<f32>> {
        let c = &self.cfg;
        let inter = layer.gate_proj.len() / c.hidden;
        x.iter().map(|t| {
            let g = matvec(&layer.gate_proj, t, inter);
            let u = matvec(&layer.up_proj, t, inter);
            let mut hmid = vec![0f32; inter];
            for ((hi, gi), ui) in hmid.iter_mut().zip(&g).zip(&u) {
                *hi = gelu(*gi) * (*ui); // GeGLU: gelu-gated, not sigmoid
            }
            matvec(&layer.down_proj, &hmid, c.hidden)
        }).collect()
    }
}

/// tanh-form logit softcap.
pub fn softcap(x: f32, cap: f32) -> f32 {
    cap * ((x / cap).tanh())
}

/// gelu (tanh approximation), used by GeGLU gates.
pub fn gelu(x: f32) -> f32 {
    const SQRT_2_OVER_PI: f32 = 0.797_884_56;
    0.5 * x * (1.0 + (SQRT_2_OVER_PI * (x + 0.044715 * x * x * x)).tanh())
}

fn masked(c: &Config, is_sliding: bool, query_pos: usize, key_pos: usize) -> bool {
    if key_pos > query_pos {
        return true; // causal
    }
    is_sliding && query_pos.saturating_sub(key_pos) >= c.sliding_window
}

pub fn softmax(scores: &[f32]) -> Vec<f32> {
    if scores.is_empty() {
        return Vec::new();
    }
    let max = scores.iter().cloned().fold(f32::NEG_INFINITY, f32::max);
    let exps: Vec<f32> = scores.iter().map(|s| (s - max).exp()).collect();
    let total: f32 = exps.iter().sum();
    exps.iter().map(|e| e / total).collect()
}

fn add_rows(base: &[Vec<f32>], delta: &[Vec<f32>]) -> Vec<Vec<f32>> {
    base.iter().zip(delta).map(|(a, b)| a.iter().zip(b).map(|(x, y)| x + y).collect()).collect()
}

fn rms_vec(v: &[f32], w: &[f32], eps: f32) -> Vec<f32> {
    let ms = v.iter().map(|x| x * x).sum::<f32>() / v.len() as f32;
    let inv = 1.0 / (ms + eps).sqrt();
    v.iter().zip(w).map(|(x, wi)| x * inv * wi).collect()
}

fn rms_norm(x: &[Vec<f32>], w: &[f32], eps: f32) -> Vec<Vec<f32>> {
    x.iter().map(|t| rms_vec(t, w, eps)).collect()
}

fn matvec(w: &[f32], x: &[f32], out_len: usize) -> Vec<f32> {
    let mut out = vec![0f32; out_len];
    for (o, row) in out.iter_mut().enumerate() {
        let off = o * x.len();
        *row = w.get(off..off + x.len())
            .map(|r| r.iter().zip(x).map(|(a, b)| a * b).sum())
            .unwrap_or(0.0);
    }
    out
}

fn rope(v: &[f32], pos: usize, theta: f32) -> Vec<f32> {
    let half = v.len() / 2;
    let mut out = v.to_vec();
    for i in 0..half {
        let freq = theta.powf(-(2.0 * i as f32) / v.len() as f32);
        let angle = freq * pos as f32;
        let (s, c) = angle.sin_cos();
        let a = v[i];
        let b = v[i + half];
        out[i] = a * c - b * s;
        out[i + half] = a * s + b * c;
    }
    out
}
