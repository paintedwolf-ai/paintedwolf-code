//! Gemma 4 E4B decoder with hybrid sliding/full attention, shared KV slots,
//! Q/K/V RMS normalization, GeGLU MLPs, per-layer embeddings, and logit softcapping.

use std::collections::HashMap;

use crate::error::{Error, Result};

pub const LOGIT_SOFTCAP: f32 = 30.0;
pub const DEFAULT_SLIDING_WINDOW: usize = 512;
pub const E4B_SOURCE_LAYERS: usize = 24;
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
    pub head_dim: usize,
    pub full_head_dim: usize,
    pub source_layers: usize,
    pub total_layers: usize,
    pub rope_theta: f32,
    pub full_rope_theta: f32,
    pub partial_rotary_factor: f32,
    pub vocab: usize,
    pub eps: f32,
    pub sliding_window: usize,
    pub full_layers_skip_rope: bool,
    pub attention_scale: f32,
    pub normalize_v: bool,
    pub ple_layers: usize,
    pub eos_tokens: Vec<u32>,
}

impl Config {
    pub fn from_json(v: &serde_json::Value) -> Result<Self> {
        let g = |k: &str| -> Result<usize> {
            v.get(k)
                .and_then(|x| x.as_u64())
                .map(|x| x as usize)
                .ok_or_else(|| Error::Checkpoint(format!("config: no {k}")))
        };
        let f = |k: &str| -> Result<f32> {
            v.get(k)
                .and_then(|x| x.as_f64())
                .map(|x| x as f32)
                .ok_or_else(|| Error::Checkpoint(format!("config: no {k}")))
        };
        let hidden = g("hidden_size")?;
        let heads = g("num_attention_heads")?;
        let kv_heads = g("num_key_value_heads")?;
        let head_dim = v
            .get("head_dim")
            .and_then(|x| x.as_u64())
            .map(|x| x as usize)
            .unwrap_or(hidden / heads);
        let full_head_dim = v
            .get("full_head_dim")
            .or_else(|| v.get("global_head_dim"))
            .and_then(|x| x.as_u64())
            .map(|x| x as usize)
            .unwrap_or(head_dim);
        let total = g("num_hidden_layers")?;
        let source = match v.get("num_kv_shared_layers") {
            Some(x) if x.is_u64() => {
                let shared = x.as_u64().unwrap() as usize;
                total.checked_sub(shared).ok_or_else(|| {
                    Error::Checkpoint(format!("config: {shared} shared layers exceed {total}"))
                })?
            }
            _ => v
                .get("num_source_layers")
                .and_then(|x| x.as_u64())
                .map(|x| x as usize)
                .unwrap_or(total),
        };
        let ple = v
            .get("num_ple_layers")
            .and_then(|x| x.as_u64())
            .map(|x| x as usize)
            .unwrap_or(0);
        if heads % kv_heads != 0 {
            return Err(Error::Checkpoint(format!(
                "config: {heads} query heads do not group into {kv_heads} kv heads"
            )));
        }
        let rope_theta = f("rope_theta")?;
        let has_partial_rotary = v.get("partial_rotary_factor").is_some()
            || v.get("full_rope_theta").is_some();
        let full_rope_theta = v
            .get("full_rope_theta")
            .and_then(|x| x.as_f64())
            .map(|x| x as f32)
            .unwrap_or(if has_partial_rotary { 1_000_000.0 } else { rope_theta });
        let partial_rotary_factor = v
            .get("partial_rotary_factor")
            .and_then(|x| x.as_f64())
            .map(|x| x as f32)
            .unwrap_or(1.0);
        let full_layers_skip_rope = v
            .get("full_layers_skip_rope")
            .and_then(|x| x.as_bool())
            .unwrap_or(!has_partial_rotary);
        let vocab = g("vocab_size")?;
        let eos_tokens = match v.get("eos_token_id") {
            Some(serde_json::Value::Number(n)) => n
                .as_u64()
                .map(|id| vec![id as u32])
                .unwrap_or_else(|| vec![vocab.saturating_sub(1) as u32]),
            Some(serde_json::Value::Array(arr)) => {
                let ids: Vec<u32> = arr
                    .iter()
                    .filter_map(|x| x.as_u64().map(|id| id as u32))
                    .collect();
                if ids.is_empty() {
                    vec![vocab.saturating_sub(1) as u32]
                } else {
                    ids
                }
            }
            _ => vec![vocab.saturating_sub(1) as u32],
        };
        Ok(Self {
            hidden,
            heads,
            kv_heads,
            head_dim,
            full_head_dim,
            source_layers: source,
            total_layers: total,
            rope_theta,
            full_rope_theta,
            partial_rotary_factor,
            vocab,
            eps: f("rms_norm_eps")?,
            sliding_window: v
                .get("sliding_window")
                .and_then(|x| x.as_u64())
                .map(|x| x as usize)
                .unwrap_or(DEFAULT_SLIDING_WINDOW),
            full_layers_skip_rope,
            attention_scale: v
                .get("attention_scale")
                .and_then(|x| x.as_f64())
                .map(|x| x as f32)
                .unwrap_or(1.0),
            normalize_v: v
                .get("normalize_v")
                .and_then(|x| x.as_bool())
                .unwrap_or(true),
            ple_layers: ple,
            eos_tokens,
        })
    }

    pub fn head_dim_for(&self, layer: usize) -> usize {
        if self.layer_kind(layer) == LayerKind::Full {
            self.full_head_dim
        } else {
            self.head_dim
        }
    }

    fn rope_applies(&self, layer: usize) -> bool {
        !(self.full_layers_skip_rope && self.layer_kind(layer) == LayerKind::Full)
    }

    fn rope_params_for(&self, layer: usize) -> (f32, usize) {
        let dim = self.head_dim_for(layer);
        if self.layer_kind(layer) == LayerKind::Full {
            let rot = ((dim as f32 * self.partial_rotary_factor).round() as usize) & !1usize;
            (self.full_rope_theta, rot.clamp(0, dim))
        } else {
            (self.rope_theta, dim)
        }
    }

    /// Hybrid 5:1 pattern: every 6th layer (indices 5, 11, 17, 23, 29, 35, 41) uses full attention.
    pub fn layer_kind(&self, layer: usize) -> LayerKind {
        if layer % 6 == 5 {
            LayerKind::Full
        } else {
            LayerKind::Sliding
        }
    }

    /// Source layers own their cache slot; shared layers reuse the last source slot of matching kind.
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

    pub fn q_width(&self, layer: usize) -> usize {
        self.heads * self.head_dim_for(layer)
    }

    pub fn kv_width(&self, layer: usize) -> usize {
        self.kv_heads * self.head_dim_for(layer)
    }
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
    pub k_proj: Option<Vec<f32>>,
    pub v_proj: Option<Vec<f32>>,
    pub o_proj: Vec<f32>,
    pub q_norm: Vec<f32>,
    pub k_norm: Option<Vec<f32>>,
    pub pre_mlp_norm: Vec<f32>,
    pub post_mlp_norm: Option<Vec<f32>>,
    pub gate_proj: Vec<f32>,
    pub up_proj: Vec<f32>,
    pub down_proj: Vec<f32>,
    pub layer_scalar: f32,
}

pub struct Decoder {
    pub cfg: Config,
    pub embed: Vec<f32>,
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
            let has_block_norms = w
                .tensors
                .contains_key(&format!("{p}.post_attention_layernorm.weight"));
            let k_norm_key = format!("{p}.self_attn.k_norm.weight");
            let k_norm = if owned || w.tensors.contains_key(&k_norm_key) {
                Some(w.take(&k_norm_key)?)
            } else {
                None
            };
            let scalar_key = format!("{p}.layer_scalar");
            let layer_scalar = w
                .tensors
                .remove(&scalar_key)
                .and_then(|v| v.first().copied())
                .unwrap_or(1.0);
            layers.push(Layer {
                pre_attn_norm: w.take(&format!("{p}.input_layernorm.weight"))?,
                post_attn_norm: if has_block_norms {
                    Some(w.take(&format!("{p}.post_attention_layernorm.weight"))?)
                } else {
                    None
                },
                q_proj: w.take(&format!("{p}.self_attn.q_proj.weight"))?,
                k_proj: if owned {
                    Some(w.take(&format!("{p}.self_attn.k_proj.weight"))?)
                } else {
                    None
                },
                v_proj: if owned {
                    Some(w.take(&format!("{p}.self_attn.v_proj.weight"))?)
                } else {
                    None
                },
                o_proj: w.take(&format!("{p}.self_attn.o_proj.weight"))?,
                q_norm: w.take(&format!("{p}.self_attn.q_norm.weight"))?,
                k_norm,
                pre_mlp_norm: w.take(&format!("{p}.pre_feedforward_layernorm.weight"))?,
                post_mlp_norm: if has_block_norms {
                    Some(w.take(&format!("{p}.post_feedforward_layernorm.weight"))?)
                } else {
                    None
                },
                gate_proj: w.take(&format!("{p}.mlp.gate_proj.weight"))?,
                up_proj: w.take(&format!("{p}.mlp.up_proj.weight"))?,
                down_proj: w.take(&format!("{p}.mlp.down_proj.weight"))?,
                layer_scalar,
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
        self.embed
            .get(off..off + c.hidden)
            .map(<[f32]>::to_vec)
            .ok_or_else(|| Error::Checkpoint(format!("token {t} out of embedding range")))
    }

    /// Run one forward step over `tokens`, returning last-position logits and newly staged KV per source slot.
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
        let mut x: Vec<Vec<f32>> = tokens
            .iter()
            .map(|&t| self.embed_one(t))
            .collect::<Result<_>>()?;

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
            if (layer.layer_scalar - 1.0).abs() > f32::EPSILON {
                for row in &mut h {
                    for val in row {
                        *val *= layer.layer_scalar;
                    }
                }
            }
            x = h;
        }

        let n = rms_norm(&x, &self.final_norm, c.eps);
        let last = n
            .last()
            .ok_or_else(|| Error::Capacity("empty hidden state".into()))?;
        let logits: Vec<f32> = (0..c.vocab)
            .map(|v| {
                let off = v * c.hidden;
                let dot: f32 = self
                    .embed
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
        let owned_kv = layer.k_proj.is_some();
        if owned_kv {
            let k_proj = layer.k_proj.as_ref().unwrap();
            let v_proj = layer.v_proj.as_ref().unwrap();
            let k_norm = layer
                .k_norm
                .as_ref()
                .ok_or_else(|| Error::Checkpoint(format!("layer {layer_idx}: missing k_norm")))?;
            let k: Vec<Vec<f32>> = h.iter().map(|t| matvec(k_proj, t, kv_w)).collect();
            let v: Vec<Vec<f32>> = h.iter().map(|t| matvec(v_proj, t, kv_w)).collect();
            let mut nk = Vec::with_capacity(seq * kv_w);
            let mut nv = Vec::with_capacity(seq * kv_w);
            let unit_weights = vec![1.0; dim];
            for (kt, vt) in k.iter().zip(&v) {
                for g in 0..c.kv_heads {
                    let head = &kt[g * dim..(g + 1) * dim];
                    let w_slice = if k_norm.len() == dim {
                        &k_norm[..]
                    } else {
                        &k_norm[g * dim..(g + 1) * dim]
                    };
                    nk.extend_from_slice(&rms_vec(head, w_slice, c.eps));
                }
                if c.normalize_v {
                    for g in 0..c.kv_heads {
                        let head = &vt[g * dim..(g + 1) * dim];
                        nv.extend_from_slice(&rms_vec(head, &unit_weights, c.eps));
                    }
                } else {
                    nv.extend_from_slice(vt);
                }
            }
            staged.push((kv_slot, nk, nv));
        }

        // Both source and shared layers read committed KV, earlier steps' staged KV in the
        // current request, and the current step's newly projected KV for `kv_slot`.
        let (ck, cv) = kv.committed_kv(kv_slot);
        let (sk, sv) = kv.staged_kv(kv_slot);
        let (step_k, step_v): (&[f32], &[f32]) = staged
            .iter()
            .rev()
            .find(|(slot, _, _)| *slot == kv_slot)
            .map(|(_, k, v)| (k.as_slice(), v.as_slice()))
            .unwrap_or((&[], &[]));

        let committed_tokens = ck.len() / kv_w;
        let staged_tokens = sk.len() / kv_w;
        let step_tokens = step_k.len() / kv_w;

        let heads_per_group = c.heads / c.kv_heads;
        let scale = c.attention_scale;
        let applies_rope = c.rope_applies(layer_idx);
        let (theta, rotary_dim) = c.rope_params_for(layer_idx);
        let is_sliding = c.layer_kind(layer_idx) == LayerKind::Sliding;

        let mut out = Vec::with_capacity(seq);
        for (s, qs) in q.iter().enumerate() {
            let pos = next_pos + s;
            let mut acc = vec![0f32; q_w];
            for g in 0..c.kv_heads {
                for qi in 0..heads_per_group {
                    let qh = g * heads_per_group + qi;
                    let q_norm_slice = if layer.q_norm.len() == dim {
                        &layer.q_norm[..]
                    } else {
                        &layer.q_norm[qh * dim..(qh + 1) * dim]
                    };
                    let qv = rms_vec(&qs[qh * dim..(qh + 1) * dim], q_norm_slice, c.eps);
                    let qv = if applies_rope {
                        rope_partial(&qv, pos, theta, rotary_dim)
                    } else {
                        qv
                    };

                    let mut scores: Vec<f32> = Vec::new();
                    let mut vs: Vec<Vec<f32>> = Vec::new();

                    let mut attend_segment =
                        |seg_k: &[f32], seg_v: &[f32], base_pos: usize, count: usize| {
                            for idx in 0..count {
                                let p = base_pos + idx;
                                if masked(c, is_sliding, pos, p) {
                                    continue;
                                }
                                let off = idx * kv_w + g * dim;
                                let k_slice = &seg_k[off..off + dim];
                                let dot: f32 = if applies_rope {
                                    let kp = rope_partial(k_slice, p, theta, rotary_dim);
                                    qv.iter().zip(&kp).map(|(a, b)| a * b).sum::<f32>() * scale
                                } else {
                                    qv.iter().zip(k_slice).map(|(a, b)| a * b).sum::<f32>() * scale
                                };
                                scores.push(dot);
                                vs.push(seg_v[off..off + dim].to_vec());
                            }
                        };

                    attend_segment(ck, cv, 0, committed_tokens);
                    attend_segment(sk, sv, committed_tokens, staged_tokens);
                    attend_segment(step_k, step_v, next_pos, step_tokens);

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
            out.push(matvec(&layer.o_proj, &acc, c.hidden));
        }
        Ok(out)
    }

    fn mlp(&self, layer: &Layer, x: &[Vec<f32>]) -> Vec<Vec<f32>> {
        let c = &self.cfg;
        let inter = layer.gate_proj.len() / c.hidden;
        x.iter()
            .map(|t| {
                let g = matvec(&layer.gate_proj, t, inter);
                let u = matvec(&layer.up_proj, t, inter);
                let mut hmid = vec![0f32; inter];
                for ((hi, gi), ui) in hmid.iter_mut().zip(&g).zip(&u) {
                    *hi = gelu(*gi) * (*ui);
                }
                matvec(&layer.down_proj, &hmid, c.hidden)
            })
            .collect()
    }
}

pub fn softcap(x: f32, cap: f32) -> f32 {
    cap * ((x / cap).tanh())
}

pub fn gelu(x: f32) -> f32 {
    const SQRT_2_OVER_PI: f32 = 0.797_884_56;
    0.5 * x * (1.0 + (SQRT_2_OVER_PI * (x + 0.044715 * x * x * x)).tanh())
}

fn masked(c: &Config, is_sliding: bool, query_pos: usize, key_pos: usize) -> bool {
    if key_pos > query_pos {
        return true;
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
    base.iter()
        .zip(delta)
        .map(|(a, b)| a.iter().zip(b).map(|(x, y)| x + y).collect())
        .collect()
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
        *row = w
            .get(off..off + x.len())
            .map(|r| r.iter().zip(x).map(|(a, b)| a * b).sum())
            .unwrap_or(0.0);
    }
    out
}

pub fn rope(v: &[f32], pos: usize, theta: f32) -> Vec<f32> {
    rope_partial(v, pos, theta, v.len())
}

pub fn rope_partial(v: &[f32], pos: usize, theta: f32, rotary_dim: usize) -> Vec<f32> {
    let rot = rotary_dim.min(v.len()) & !1usize;
    let half = rot / 2;
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cache::Cache;

    fn synthetic_hybrid_decoder(source_layers: usize, shared_layers: usize) -> Decoder {
        let total_layers = source_layers + shared_layers;
        let cfg = Config {
            hidden: 8,
            heads: 4,
            kv_heads: 2,
            head_dim: 4,
            full_head_dim: 8,
            source_layers,
            total_layers,
            rope_theta: 10_000.0,
            full_rope_theta: 1_000_000.0,
            partial_rotary_factor: 0.25,
            vocab: 6,
            eps: 1e-5,
            sliding_window: 3,
            full_layers_skip_rope: false,
            attention_scale: 1.0,
            normalize_v: true,
            ple_layers: 1,
            eos_tokens: vec![5],
        };

        let mut tensors = HashMap::new();
        let fill = |len: usize, seed: usize| -> Vec<f32> {
            (0..len)
                .map(|i| (((i + seed * 17) % 13) as f32 - 6.0) * 0.05)
                .collect()
        };
        tensors.insert("model.embed_tokens.weight".into(), fill(cfg.vocab * cfg.hidden, 1));
        tensors.insert("model.norm.weight".into(), vec![1.0; cfg.hidden]);
        tensors.insert("model.ple.0.weight".into(), fill(16 * cfg.hidden, 2));

        let inter = 12;
        for i in 0..total_layers {
            let p = format!("model.layers.{i}");
            let owned = i < source_layers;
            let dim = cfg.head_dim_for(i);
            let q_w = cfg.q_width(i);
            let kv_w = cfg.kv_width(i);

            tensors.insert(format!("{p}.input_layernorm.weight"), vec![1.0; cfg.hidden]);
            tensors.insert(format!("{p}.post_attention_layernorm.weight"), vec![1.0; cfg.hidden]);
            tensors.insert(format!("{p}.pre_feedforward_layernorm.weight"), vec![1.0; cfg.hidden]);
            tensors.insert(format!("{p}.post_feedforward_layernorm.weight"), vec![1.0; cfg.hidden]);
            tensors.insert(format!("{p}.self_attn.q_proj.weight"), fill(q_w * cfg.hidden, i + 3));
            if owned {
                tensors.insert(format!("{p}.self_attn.k_proj.weight"), fill(kv_w * cfg.hidden, i + 10));
                tensors.insert(format!("{p}.self_attn.v_proj.weight"), fill(kv_w * cfg.hidden, i + 20));
                tensors.insert(format!("{p}.self_attn.k_norm.weight"), vec![1.0; dim]);
            }
            tensors.insert(format!("{p}.self_attn.o_proj.weight"), fill(cfg.hidden * q_w, i + 30));
            tensors.insert(format!("{p}.self_attn.q_norm.weight"), vec![1.0; dim]);
            tensors.insert(format!("{p}.mlp.gate_proj.weight"), fill(inter * cfg.hidden, i + 40));
            tensors.insert(format!("{p}.mlp.up_proj.weight"), fill(inter * cfg.hidden, i + 50));
            tensors.insert(format!("{p}.mlp.down_proj.weight"), fill(cfg.hidden * inter, i + 60));
        }

        Decoder::load(cfg, Weights { tensors }).expect("load synthetic hybrid decoder")
    }

    #[test]
    fn e4b_shared_layer_mapping_targets_slots_22_and_23() {
        let v = serde_json::json!({
            "hidden_size": 2560,
            "num_attention_heads": 8,
            "num_key_value_heads": 2,
            "head_dim": 256,
            "full_head_dim": 512,
            "num_hidden_layers": 42,
            "num_kv_shared_layers": 18,
            "rope_theta": 10000.0,
            "full_rope_theta": 1000000.0,
            "partial_rotary_factor": 0.25,
            "vocab_size": 262144,
            "rms_norm_eps": 1e-6,
            "sliding_window": 512
        });
        let cfg = Config::from_json(&v).unwrap();
        assert_eq!(cfg.source_layers, E4B_SOURCE_LAYERS);
        assert_eq!(cfg.total_layers - cfg.source_layers, E4B_SHARED_LAYERS);
        for layer in 24..42 {
            let expected = if layer % 6 == 5 { 23 } else { 22 };
            assert_eq!(cfg.cache_slot(layer), expected, "layer {layer}");
        }
    }

    #[test]
    fn prefill_matches_incremental_decode_across_hybrid_and_shared_layers() {
        let dec = synthetic_hybrid_decoder(6, 6);
        let slot_widths: Vec<usize> = (0..dec.cfg.source_layers)
            .map(|s| dec.cfg.kv_width(s))
            .collect();

        let prompt = [1u32, 2, 3, 4];
        let mut batch_cache = Cache::with_slot_widths(slot_widths.clone());
        let (batch_logits, batch_kv) = dec.step(&prompt, &batch_cache, 0).unwrap();
        batch_cache.stage(&batch_kv).unwrap();
        batch_cache.commit(batch_logits.clone());

        let mut step_cache = Cache::with_slot_widths(slot_widths);
        let mut last_logits = Vec::new();
        for &tok in &prompt {
            let pos = step_cache.provisional_len();
            let (logits, kv) = dec.step(&[tok], &step_cache, pos).unwrap();
            step_cache.stage(&kv).unwrap();
            last_logits = logits;
        }
        step_cache.commit(last_logits.clone());

        for (i, (a, b)) in batch_logits.iter().zip(&last_logits).enumerate() {
            assert!(
                (a - b).abs() < 1e-4,
                "logit {i} diverged between prefill ({a}) and step-by-step ({b})"
            );
        }
    }

    #[test]
    fn softcap_bounds_extreme_logits() {
        assert!((softcap(1000.0, LOGIT_SOFTCAP) - LOGIT_SOFTCAP).abs() < 1e-4);
        assert!((softcap(-1000.0, LOGIT_SOFTCAP) + LOGIT_SOFTCAP).abs() < 1e-4);
        assert!((softcap(0.0, LOGIT_SOFTCAP)).abs() < 1e-6);
    }

    #[test]
    fn proportional_rope_rotates_only_configured_prefix() {
        let v = vec![1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0];
        let out = rope_partial(&v, 3, 1_000_000.0, 2);
        assert_ne!(&out[..2], &v[..2]);
        assert_eq!(&out[2..], &v[2..]);
    }
}
