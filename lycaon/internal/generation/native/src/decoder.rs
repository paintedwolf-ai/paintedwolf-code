//! The Gemma 4 E4B decoder on CPU, kept exact so accelerated backends have a reference.
//!
//! Topology follows the hybrid layout: 24 source layers own KV (20 sliding, 4 full) and
//! later layers reuse their source layer's cache slot. RMSNorm is unscaled on V; the
//! final logits pass through a softcap of 30.

use std::collections::HashMap;

use crate::error::{Error, Result};

pub const LOGIT_SOFTCAP: f32 = 30.0;
pub const SLIDING_WINDOW: usize = 512;
pub const QUERY_HEADS_PER_GROUP: usize = 1;

pub struct Config {
    pub hidden: usize,
    pub heads: usize,
    pub kv_heads: usize,
    pub head_dim: usize,
    pub source_layers: usize,
    pub total_layers: usize,
    pub rope_theta: f32,
    pub vocab: usize,
    pub eps: f32,
}

impl Config {
    pub fn from_json(v: &serde_json::Value) -> Result<Self> {
        let g = |k: &str| -> Result<usize> {
            v.get(k).and_then(|x| x.as_u64()).map(|x| x as usize).ok_or_else(|| Error::Checkpoint(format!("config: no {k}")))
        };
        let f = |k: &str| -> Result<f32> {
            v.get(k).and_then(|x| x.as_f64()).map(|x| x as f32).ok_or_else(|| Error::Checkpoint(format!("config: no {k}")))
        };
        let hidden = g("hidden_size")?;
        let heads = g("num_attention_heads")?;
        let kv_heads = g("num_key_value_heads")?;
        let head_dim = v
            .get("head_dim")
            .and_then(|x| x.as_u64())
            .map(|x| x as usize)
            .unwrap_or(hidden / heads);
        let total = g("num_hidden_layers")?;
        let source = v
            .get("num_kv_shared_layers")
            .and_then(|x| x.as_u64())
            .map(|x| x as usize)
            .filter(|&n| n > 0 && n <= total)
            .unwrap_or(total);
        Ok(Self {
            hidden,
            heads,
            kv_heads,
            head_dim,
            source_layers: source,
            total_layers: total,
            rope_theta: f("rope_theta")?,
            vocab: g("vocab_size")?,
            eps: f("rms_norm_eps")?,
        })
    }
}

/// All weights live in f32; a converted checkpoint is already in this layout.
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
    pub q_proj: Vec<f32>,
    pub k_proj: Vec<f32>,
    pub v_proj: Vec<f32>,
    pub o_proj: Vec<f32>,
    pub q_norm: Vec<f32>,
    pub k_norm: Vec<f32>,
    pub pre_mlp_norm: Vec<f32>,
    pub gate_proj: Vec<f32>,
    pub up_proj: Vec<f32>,
    pub down_proj: Vec<f32>,
    pub is_sliding: bool,
}

pub struct Decoder {
    pub cfg: Config,
    pub embed: Vec<f32>,
    pub layers: Vec<Layer>,
    pub final_norm: Vec<f32>,
}

impl Decoder {
    pub fn load(cfg: Config, mut w: Weights) -> Result<Self> {
        let mut layers = Vec::with_capacity(cfg.total_layers);
        for i in 0..cfg.total_layers {
            let p = format!("model.layers.{i}");
            layers.push(Layer {
                pre_attn_norm: w.take(&format!("{p}.input_layernorm.weight"))?,
                q_proj: w.take(&format!("{p}.self_attn.q_proj.weight"))?,
                k_proj: w.take(&format!("{p}.self_attn.k_proj.weight"))?,
                v_proj: w.take(&format!("{p}.self_attn.v_proj.weight"))?,
                o_proj: w.take(&format!("{p}.self_attn.o_proj.weight"))?,
                q_norm: w.take(&format!("{p}.self_attn.q_norm.weight"))?,
                k_norm: w.take(&format!("{p}.self_attn.k_norm.weight"))?,
                pre_mlp_norm: w.take(&format!("{p}.pre_feedforward_layernorm.weight"))?,
                gate_proj: w.take(&format!("{p}.mlp.gate_proj.weight"))?,
                up_proj: w.take(&format!("{p}.mlp.up_proj.weight"))?,
                down_proj: w.take(&format!("{p}.mlp.down_proj.weight"))?,
                is_sliding: i >= cfg.source_layers && (i - cfg.source_layers) % 2 == 0,
            });
        }
        Ok(Self {
            embed: w.take("model.embed_tokens.weight")?,
            layers,
            final_norm: w.take("model.norm.weight")?,
            cfg,
        })
    }

    /// One decode step over the current sequence. Returns logits for the last position
    /// plus per-source-layer kv to stage.
    pub fn step(&self, tokens: &[u32], kv: &crate::cache::Cache, next_pos: usize) -> Result<(Vec<f32>, Vec<(usize, Vec<f32>, Vec<f32>)>)> {
        if tokens.is_empty() {
            return Err(Error::Capacity("empty sequence".into()));
        }
        let c = &self.cfg;
        let mut x: Vec<Vec<f32>> = tokens
            .iter()
            .map(|&t| {
                let off = t as usize * c.hidden;
                self.embed.get(off..off + c.hidden).map(<[f32]>::to_vec).ok_or_else(|| Error::Checkpoint("token id out of range".into()))
            })
            .collect::<Result<Vec<_>>>()?;

        let mut staged = Vec::new();
        for (i, layer) in self.layers.iter().enumerate() {
            let kv_layer = if i < c.source_layers { i } else if layer.is_sliding { c.source_layers - 2 } else { c.source_layers - 1 };
            let h = rms_norm(&x, &layer.pre_attn_norm, c.eps);
            let attn = self.attention(&h, kv, kv_layer, next_pos, layer.is_sliding, i, &mut staged)?;
            let attn_out = residual(&x, &attn);
            let m = rms_norm(&attn_out, &layer.pre_mlp_norm, c.eps);
            let mlp = self.mlp(layer, &m)?;
            x = residual(&attn_out, &mlp);
        }

        let n = rms_norm(&x, &self.final_norm, c.eps);
        let last = n.last().ok_or_else(|| Error::Capacity("empty hidden state".into()))?;
        let logits: Vec<f32> = (0..c.vocab)
            .map(|v| {
                let off = v * c.hidden;
                let dot: f32 = self
                    .embed
                    .get(off..off + c.hidden)
                    .map(|row| row.iter().zip(last).map(|(a, b)| a * b).sum())
                    .unwrap_or(0.0);
                (dot / (1.0 + dot.abs())).min(LOGIT_SOFTCAP)
            })
            .collect();
        Ok((logits, staged))
    }

    #[allow(clippy::too_many_arguments)]
    fn attention(&self, h: &[Vec<f32>], kv: &crate::cache::Cache, kv_layer: usize, next_pos: usize, is_sliding: bool, layer_idx: usize, staged: &mut Vec<(usize, Vec<f32>, Vec<f32>)>) -> Result<Vec<Vec<f32>>> {
        let c = &self.cfg;
        let seq = h.len();
        // Project q, k, v. GQA expands kv heads to query head count.
        let q: Vec<Vec<f32>> = h.iter().map(|t| matvec(&self.layers[layer_idx].q_proj, t, c.hidden * c.heads)).collect();
        let k: Vec<Vec<f32>> = h.iter().map(|t| matvec(&self.layers[layer_idx].k_proj, t, c.kv_heads * c.head_dim)).collect();
        let v: Vec<Vec<f32>> = h.iter().map(|t| matvec(&self.layers[layer_idx].v_proj, t, c.kv_heads * c.head_dim)).collect();

        // New kv becomes the cache's staged tail; attention reads committed + staged.
        let mut new_k = Vec::with_capacity(seq * c.kv_heads * c.head_dim);
        let mut new_v = Vec::with_capacity(seq * c.kv_heads * c.head_dim);
        for (kt, vt) in k.iter().zip(&v) {
            new_k.extend_from_slice(&rms_vec(kt, &self.layers[layer_idx].k_norm, c.eps));
            new_v.extend_from_slice(vt);
        }
        staged.push((kv_layer, new_k.clone(), new_v.clone()));

        let mut out = Vec::with_capacity(seq);
        for (s, qs) in q.iter().enumerate() {
            let pos = next_pos + s;
            let mut acc = vec![0f32; c.heads * c.head_dim];
            for g in 0..c.kv_heads {
                let scale = 1.0; // Gemma 4 normalizes q, so the scale stays 1.
                for qi in 0..QUERY_HEADS_PER_GROUP {
                    let qh = g * QUERY_HEADS_PER_GROUP + qi;
                    let qv = rms_vec(&qs[qh * c.head_dim..(qh + 1) * c.head_dim], &self.layers[layer_idx].q_norm, c.eps);
                    let qv = rope(&qv, pos, c.rope_theta);
                    let mut best = vec![0f32; c.head_dim];
                    let mut total = 0.0;
                    // Committed positions then the staged tail.
                    let committed = kv.committed_len().min(pos + 1);
                    for p in 0..committed {
                        if is_sliding && pos.saturating_sub(p) >= SLIDING_WINDOW {
                            continue;
                        }
                        let kvd = kv.committed_kv(kv_layer);
                        let kp = &kvd.0[p * c.head_dim..(p + 1) * c.head_dim];
                        let kp = rope(kp, p, c.rope_theta);
                        let dot: f32 = qv.iter().zip(&kp).map(|(a, b)| a * b).sum();
                        total += dot;
                        for (bi, bv) in best.iter_mut().zip(&kvd.1[p * c.head_dim..(p + 1) * c.head_dim]) {
                            *bi += dot * bv;
                        }
                    }
                    for s2 in 0..seq {
                        let p = next_pos + s2;
                        if is_sliding && pos.saturating_sub(p) >= SLIDING_WINDOW {
                            continue;
                        }
                        let off = s2 * c.kv_heads * c.head_dim + g * c.head_dim;
                        let kp = rope(&new_k[off..off + c.head_dim].to_vec(), p, c.rope_theta);
                        let dot: f32 = qv.iter().zip(&kp).map(|(a, b)| a * b).sum::<f32>() * scale;
                        total += dot;
                        for (bi, bv) in best.iter_mut().zip(&new_v[off..off + c.head_dim]) {
                            *bi += dot * bv;
                        }
                    }
                    for bi in &mut best {
                        *bi /= total.max(1e-9);
                    }
                    acc[qh * c.head_dim..(qh + 1) * c.head_dim].copy_from_slice(&best);
                }
            }
            let mut row = vec![0f32; c.hidden];
            for (qh, chunk) in acc.chunks(c.head_dim).enumerate() {
                for (d, val) in chunk.iter().enumerate() {
                    row[qh * c.head_dim + d] = *val;
                }
            }
            out.push(matvec(&self.layers[layer_idx].o_proj, &row, c.hidden * c.hidden));
        }
        Ok(out)
    }

    fn mlp(&self, layer: &Layer, x: &[Vec<f32>]) -> Result<Vec<Vec<f32>>> {
        let c = &self.cfg;
        let inter = layer.gate_proj.len() / c.hidden;
        Ok(x.iter().map(|t| {
            let g = matvec(&layer.gate_proj, t, inter);
            let u = matvec(&layer.up_proj, t, inter);
            let mut h = vec![0f32; inter];
            for ((hi, gi), ui) in h.iter_mut().zip(&g).zip(&u) {
                *hi = gi * (1.0 / (1.0 + (-ui).exp())); // geglu
            }
            matvec(&layer.down_proj, &h, c.hidden * inter)
        }).collect())
    }
}

fn rms_vec(v: &[f32], w: &[f32], eps: f32) -> Vec<f32> {
    let ms = v.iter().map(|x| x * x).sum::<f32>() / v.len() as f32;
    let inv = 1.0 / (ms + eps).sqrt();
    v.iter().zip(w).map(|(x, wi)| x * inv * wi).collect()
}

fn rms_norm(x: &[Vec<f32>], w: &[f32], eps: f32) -> Vec<Vec<f32>> {
    x.iter().map(|t| rms_vec(t, w, eps)).collect()
}

fn residual(base: &[Vec<f32>], delta: &[Vec<f32>]) -> Vec<Vec<f32>> {
    base.iter().zip(delta).map(|(a, b)| a.iter().zip(b).map(|(x, y)| x + y).collect()).collect()
}

fn matvec(w: &[f32], x: &[f32], out_len: usize) -> Vec<f32> {
    let mut out = vec![0f32; out_len];
    for (o, row) in out.iter_mut().enumerate() {
        let off = o * x.len();
        *row = w.get(off..off + x.len()).map(|r| r.iter().zip(x).map(|(a, b)| a * b).sum()).unwrap_or(0.0);
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
