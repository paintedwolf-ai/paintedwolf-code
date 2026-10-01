//! The decision model on MLX, the runtime for Apple silicon (`--device mlx`, what `auto`
//! picks there). It mirrors the candle port in `modernbert.rs` and `model.rs` op for op, so a
//! checkpoint answers the same on either runtime up to rounding; the parity test checks that.
//!
//! MLX's kernels live in `mlx.metallib`, found beside the executable or at the path
//! [`set_metallib`] names.

use std::collections::HashMap;
use std::path::Path;

use mlx_rs::{Array, Dtype, fast, ops};

use crate::error::{Error, Result};
use crate::model::{HEAD_LN_EPS, HeadShape, MASKED_OPTION_LOGIT, MASKED_SCORE, layer_indices};
use crate::modernbert::{Config, MASK_FILL};
use crate::sequence::Batch;

/// Freed GPU memory MLX may keep for reuse. Requests differ in batch size and sequence
/// length, so freed buffers rarely fit the next allocation and an uncapped cache grows toward
/// the device's working set.
pub const CACHE_LIMIT_BYTES: usize = 512 << 20;

/// Tell MLX where its Metal library lives before the first kernel runs.
pub fn set_metallib(path: &Path) -> Result<()> {
    mlx_rs::metal::set_metallib_path(path.to_string_lossy())
        .map_err(|e| Error::Device(format!("metallib {}: {e}", path.display())))
}

/// A linear layer stored with its weight already transposed to `[in, out]`.
#[derive(Clone)]
struct Linear {
    weight_t: Array,
    bias: Option<Array>,
}

impl Linear {
    fn load(weights: &mut Weights, prefix: &str, bias: bool) -> Result<Self> {
        let weight_t = weights.take(&format!("{prefix}.weight"))?.t();
        let bias = if bias { Some(weights.take(&format!("{prefix}.bias"))?) } else { None };
        Ok(Self { weight_t, bias })
    }

    fn forward(&self, x: &Array) -> Result<Array> {
        let y = x.matmul(&self.weight_t)?;
        match &self.bias {
            Some(b) => Ok(y.add(b)?),
            None => Ok(y),
        }
    }
}

#[derive(Clone)]
struct LayerNorm {
    weight: Array,
    bias: Option<Array>,
    eps: f32,
}

impl LayerNorm {
    fn load(weights: &mut Weights, prefix: &str, bias: bool, eps: f32) -> Result<Self> {
        Ok(Self {
            weight: weights.take(&format!("{prefix}.weight"))?,
            bias: if bias { Some(weights.take(&format!("{prefix}.bias"))?) } else { None },
            eps,
        })
    }

    fn forward(&self, x: &Array) -> Result<Array> {
        Ok(fast::layer_norm(x, Some(&self.weight), self.bias.as_ref(), self.eps)?)
    }
}

/// The checkpoint's tensors, cast to the runtime dtype as they are taken.
struct Weights {
    tensors: HashMap<String, Array>,
    dtype: Dtype,
    label: String,
}

impl Weights {
    fn take(&mut self, name: &str) -> Result<Array> {
        let t = self
            .tensors
            .remove(name)
            .ok_or_else(|| Error::Checkpoint(format!("{}: no tensor {name}", self.label)))?;
        Ok(t.as_dtype(self.dtype)?)
    }

    fn has(&self, name: &str) -> bool {
        self.tensors.contains_key(name)
    }
}

struct Attention {
    qkv: Linear,
    proj: Linear,
    heads: i32,
    head_dim: i32,
    rope_theta: f32,
}

impl Attention {
    fn forward(&self, x: &Array, mask: &Array) -> Result<Array> {
        let shape = x.shape();
        let (b, l, d) = (shape[0], shape[1], shape[2]);
        let qkv = self
            .qkv
            .forward(x)?
            .reshape(&[b, l, 3, self.heads, self.head_dim])?
            .transpose_axes(&[2, 0, 3, 1, 4])?;
        let parts = qkv.split_equal(3, 0)?;
        let q = parts[0].squeeze_axes(&[0])?;
        let k = parts[1].squeeze_axes(&[0])?;
        let v = parts[2].squeeze_axes(&[0])?;
        // Non-interleaved rotation, as the candle port applies it.
        let q = fast::rope(&q, self.head_dim, false, Some(self.rope_theta), 1.0, 0, None)?;
        let k = fast::rope(&k, self.head_dim, false, Some(self.rope_theta), 1.0, 0, None)?;
        let scale = (self.head_dim as f32).powf(-0.5);
        let att = fast::scaled_dot_product_attention(&q, &k, &v, scale, mask, None)?;
        let out = att.swap_axes(1, 2)?.reshape(&[b, l, d])?;
        self.proj.forward(&out)
    }
}

struct Layer {
    attn_norm: Option<LayerNorm>,
    attn: Attention,
    mlp_norm: LayerNorm,
    wi: Linear,
    wo: Linear,
    local: bool,
}

impl Layer {
    fn forward(&self, x: &Array, global_mask: &Array, local_mask: &Array) -> Result<Array> {
        let h = match &self.attn_norm {
            Some(norm) => norm.forward(x)?,
            None => x.clone(),
        };
        let mask = if self.local { local_mask } else { global_mask };
        let x = x.add(&self.attn.forward(&h, mask)?)?;
        let m = self.wi.forward(&self.mlp_norm.forward(&x)?)?;
        let halves = m.split_equal(2, -1)?;
        let mlp = gelu(&halves[0])?.multiply(&halves[1])?;
        Ok(x.add(&self.wo.forward(&mlp)?)?)
    }
}

/// The ModernBERT backbone.
struct Backbone {
    tok: Array,
    norm: LayerNorm,
    layers: Vec<Layer>,
    final_norm: LayerNorm,
    window: i32,
    dtype: Dtype,
}

impl Backbone {
    fn load(weights: &mut Weights, cfg: &Config) -> Result<Self> {
        let eps = cfg.layer_norm_eps as f32;
        let heads = cfg.num_attention_heads as i32;
        let head_dim = (cfg.hidden_size / cfg.num_attention_heads) as i32;
        let mut layers = Vec::with_capacity(cfg.num_hidden_layers);
        for i in 0..cfg.num_hidden_layers {
            let local = i % cfg.global_attn_every_n_layers != 0;
            let p = format!("encoder.layers.{i}");
            let attn_norm = if weights.has(&format!("{p}.attn_norm.weight")) {
                Some(LayerNorm::load(weights, &format!("{p}.attn_norm"), false, eps)?)
            } else {
                None
            };
            layers.push(Layer {
                attn_norm,
                attn: Attention {
                    qkv: Linear::load(weights, &format!("{p}.attn.Wqkv"), false)?,
                    proj: Linear::load(weights, &format!("{p}.attn.Wo"), false)?,
                    heads,
                    head_dim,
                    rope_theta: if local { cfg.local_rope_theta } else { cfg.global_rope_theta }
                        as f32,
                },
                mlp_norm: LayerNorm::load(weights, &format!("{p}.mlp_norm"), false, eps)?,
                wi: Linear::load(weights, &format!("{p}.mlp.Wi"), false)?,
                wo: Linear::load(weights, &format!("{p}.mlp.Wo"), false)?,
                local,
            });
        }
        Ok(Self {
            tok: weights.take("encoder.embeddings.tok_embeddings.weight")?,
            norm: LayerNorm::load(weights, "encoder.embeddings.norm", false, eps)?,
            layers,
            final_norm: LayerNorm::load(weights, "encoder.final_norm", false, eps)?,
            window: (cfg.local_attention / 2) as i32,
            dtype: weights.dtype,
        })
    }

    /// `[B, L]` token ids and a `[B, L]` 0/1 mask to `[B, L, D]` hidden states.
    fn forward(&self, ids: &Array, attention_mask: &Array) -> Result<Array> {
        let (b, l) = (ids.shape()[0], ids.shape()[1]);
        // Additive masks: 0 where a query may look, MASK_FILL where it may not; faster than
        // boolean masks on the fused kernel at these shapes.
        let global = padding_bias(attention_mask, MASK_FILL as f32, self.dtype)?;
        let local = global.add(&window_bias(l, self.window, self.dtype)?)?;
        let mut x = self.norm.forward(&self.tok.take_axis(ids, 0)?)?;
        for layer in &self.layers {
            x = layer.forward(&x, &global, &local)?;
        }
        debug_assert_eq!(x.shape()[0], b);
        self.final_norm.forward(&x)
    }
}

/// The exact (erf) GELU as plain ops: the crate's compiled GELU recompiles for every new
/// input shape, and every turn has its own sequence length.
fn gelu(x: &Array) -> Result<Array> {
    let scaled = x.multiply(Array::from_f32(std::f32::consts::FRAC_1_SQRT_2))?;
    let cdf = ops::erf(&scaled)?.add(Array::from_f32(1.0))?.multiply(Array::from_f32(0.5))?;
    Ok(x.multiply(&cdf)?)
}

/// `[B, L]` 0/1 → `[B, 1, 1, L]` with `fill` on the zeros.
fn padding_bias(mask: &Array, fill: f32, dtype: Dtype) -> Result<Array> {
    let (b, l) = (mask.shape()[0], mask.shape()[1]);
    let inverted = Array::from_f32(1.0).subtract(mask)?.multiply(Array::from_f32(fill))?;
    Ok(inverted.reshape(&[b, 1, 1, l])?.as_dtype(dtype)?)
}

/// `[1, 1, L, L]` with `MASK_FILL` wherever two positions are more than `window` apart.
fn window_bias(l: i32, window: i32, dtype: Dtype) -> Result<Array> {
    let mut values = Vec::with_capacity((l * l) as usize);
    for i in 0..l {
        for j in 0..l {
            values.push(if (j - i).abs() > window { MASK_FILL as f32 } else { 0.0 });
        }
    }
    Ok(Array::from_slice(&values, &[1, 1, l, l]).as_dtype(dtype)?)
}

#[derive(Clone)]
struct HeadLayer {
    norm1: LayerNorm,
    in_proj: Linear,
    out_proj: Linear,
    norm2: LayerNorm,
    linear1: Linear,
    linear2: Linear,
    heads: i32,
    head_dim: i32,
}

impl HeadLayer {
    fn load(weights: &mut Weights, prefix: &str, hidden: usize) -> Result<Self> {
        let heads = std::cmp::max(1, hidden / 64) as i32;
        Ok(Self {
            norm1: LayerNorm::load(weights, &format!("{prefix}.norm1"), true, HEAD_LN_EPS as f32)?,
            in_proj: Linear {
                weight_t: weights.take(&format!("{prefix}.self_attn.in_proj_weight"))?.t(),
                bias: Some(weights.take(&format!("{prefix}.self_attn.in_proj_bias"))?),
            },
            out_proj: Linear::load(weights, &format!("{prefix}.self_attn.out_proj"), true)?,
            norm2: LayerNorm::load(weights, &format!("{prefix}.norm2"), true, HEAD_LN_EPS as f32)?,
            linear1: Linear::load(weights, &format!("{prefix}.linear1"), true)?,
            linear2: Linear::load(weights, &format!("{prefix}.linear2"), true)?,
            heads,
            head_dim: hidden as i32 / heads,
        })
    }

    fn forward(&self, x: &Array, bias: &Array) -> Result<Array> {
        let shape = x.shape();
        let (b, l, d) = (shape[0], shape[1], shape[2]);
        let qkv = self.in_proj.forward(&self.norm1.forward(x)?)?;
        let parts = qkv.split_equal(3, -1)?;
        let split = |p: &Array| -> Result<Array> {
            Ok(p.reshape(&[b, l, self.heads, self.head_dim])?.swap_axes(1, 2)?)
        };
        let (q, k, v) = (split(&parts[0])?, split(&parts[1])?, split(&parts[2])?);
        let scale = (self.head_dim as f32).powf(-0.5);
        let att = fast::scaled_dot_product_attention(&q, &k, &v, scale, bias, None)?;
        let out = self.out_proj.forward(&att.swap_axes(1, 2)?.reshape(&[b, l, d])?)?;
        let x = x.add(&out)?;
        let ff = self.linear2.forward(&ops::maximum(
            &self.linear1.forward(&self.norm2.forward(&x)?)?,
            Array::from_f32(0.0),
        )?)?;
        Ok(x.add(&ff)?)
    }
}

/// One typed decision head on the MLX runtime.
#[derive(Clone)]
pub(crate) struct Head {
    layers: Vec<HeadLayer>,
    type_emb: Array,
    scorer_norm: LayerNorm,
    scorer_fc1: Linear,
    scorer_fc2: Linear,
}

impl Head {
    fn load(weights: &mut Weights, shape: HeadShape) -> Result<Self> {
        let found = layer_indices(weights.tensors.keys(), "head.layers.");
        if found != (0..shape.layers).collect::<Vec<_>>() {
            return Err(Error::Head(format!(
                "{}: the backbone's heads have {} layers, but this head has layers {found:?}",
                weights.label, shape.layers
            )));
        }
        let layers = (0..shape.layers)
            .map(|i| HeadLayer::load(weights, &format!("head.layers.{i}"), shape.hidden))
            .collect::<Result<Vec<_>>>()?;
        Ok(Self {
            layers,
            type_emb: weights.take("type_emb.weight")?,
            scorer_norm: LayerNorm::load(weights, "scorer.0", true, HEAD_LN_EPS as f32)?,
            scorer_fc1: Linear::load(weights, "scorer.1", true)?,
            scorer_fc2: Linear::load(weights, "scorer.3", true)?,
        })
    }

    /// Build a head from a head file.
    pub(crate) fn from_file(path: &Path, shape: HeadShape, dtype: Dtype) -> Result<Self> {
        let tensors = Array::load_safetensors(path)
            .map_err(|e| Error::Head(format!("{}: {e}", path.display())))?;
        let label = path.display().to_string();
        for prefix in ["head.layers.", "scorer.", "type_emb."] {
            if !tensors.keys().any(|k| k.starts_with(prefix)) {
                return Err(Error::Head(format!(
                    "{label}: no {prefix}* tensors, so it is not a decision head"
                )));
            }
        }
        Self::load(&mut Weights { tensors, dtype, label }, shape)
    }
}

/// The resident backbone and the checkpoint's own head, on MLX.
pub(crate) struct Model {
    backbone: Backbone,
    base: Head,
    shape: HeadShape,
    dtype: Dtype,
}

impl Model {
    /// Load `model.safetensors` (already verified by name) and the encoder config.
    pub(crate) fn load(
        weights: HashMap<String, Array>,
        enc_cfg: &Config,
        head_layers: usize,
        dtype: Dtype,
        label: &str,
    ) -> Result<Self> {
        mlx_rs::memory::set_cache_limit(CACHE_LIMIT_BYTES)?;
        let shape = HeadShape { hidden: enc_cfg.hidden_size, layers: head_layers };
        let mut weights = Weights { tensors: weights, dtype, label: label.to_string() };
        let backbone = Backbone::load(&mut weights, enc_cfg)?;
        let base = Head::load(&mut weights, shape)?;
        Ok(Self { backbone, base, shape, dtype })
    }

    pub(crate) fn shape(&self) -> HeadShape {
        self.shape
    }

    pub(crate) fn dtype(&self) -> Dtype {
        self.dtype
    }

    pub(crate) fn base_head(&self) -> &Head {
        &self.base
    }

    /// Run the backbone and `head` over one collated batch: `[B, K]` logits per row.
    pub(crate) fn forward(&self, head: &Head, batch: &Batch) -> Result<crate::model::Forward> {
        let (b, l, k) = (batch.batch as i32, batch.seq_len as i32, batch.k_max as i32);
        let ids = Array::from_slice(&batch.input_ids, &[b, l]);
        let attention_mask = Array::from_slice(&batch.attention_mask, &[b, l]);
        let marker_pos = Array::from_slice(&batch.marker_pos, &[b, k]);
        let marker_mask = Array::from_slice(
            &batch.marker_mask.iter().map(|&m| m != 0).collect::<Vec<bool>>(),
            &[b, k],
        );
        let qtype = Array::from_slice(&batch.qtype, &[b]);

        let hs = self.backbone.forward(&ids, &attention_mask)?;
        // The question type is a global signal, so it is added to every position.
        let type_vec = head.type_emb.take_axis(&qtype, 0)?.expand_dims(1)?;
        let mut hs = hs.add(&type_vec)?;
        // Padding is masked out of the head's attention exactly as `src_key_padding_mask` does.
        let bias = padding_bias(&attention_mask, MASKED_SCORE as f32, self.dtype)?;
        for layer in &head.layers {
            hs = layer.forward(&hs, &bias)?;
        }
        // One hidden state per marker, then a scalar score per option.
        let hidden = self.shape.hidden as i32;
        let idx = ops::broadcast_to(marker_pos.expand_dims(2)?, &[b, k, hidden])?;
        let markers = hs.take_along_axis(&idx, 1)?;
        let scored = head
            .scorer_fc2
            .forward(&gelu(&head.scorer_fc1.forward(&head.scorer_norm.forward(&markers)?)?)?)?;
        let logits = scored.squeeze_axes(&[2])?.as_dtype(Dtype::Float32)?;
        let masked = ops::full::<f32>(&[b, k], Array::from_f32(MASKED_OPTION_LOGIT))?;
        let logits = ops::select(&marker_mask, &logits, &masked)?;
        logits.eval()?;
        let flat = logits.as_slice::<f32>();
        Ok(flat.chunks(k as usize).map(|row| row.to_vec()).collect())
    }
}

impl From<mlx_rs::error::Exception> for Error {
    fn from(e: mlx_rs::error::Exception) -> Self {
        Error::Mlx(e.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::device::DeviceChoice;
    use crate::engine::{Engine, EngineOptions, HEAD_TURN_LOAD};
    use crate::question::Question;
    use crate::testutil::{scratch_dir, write_tiny_checkpoint};
    use indexmap::IndexMap;
    use serde_json::Value;

    fn options(dir: &Path, device: DeviceChoice) -> EngineOptions {
        EngineOptions {
            model_dir: dir.to_path_buf(),
            device,
            dtype: "f32".to_string(),
            ..Default::default()
        }
    }

    #[test]
    fn mlx_matches_candle_on_the_same_checkpoint() {
        let dir = scratch_dir("mlx-parity");
        write_tiny_checkpoint(&dir);
        let candle = Engine::load(&options(&dir, DeviceChoice::Cpu)).unwrap();
        let mlx = Engine::load(&options(&dir, DeviceChoice::Mlx)).unwrap();
        assert_eq!(mlx.identity().device, "mlx");
        let questions: IndexMap<String, Question> = [
            (
                "pick".to_string(),
                Question::choice("pick one")
                    .bare_option("alpha")
                    .bare_option("beta")
                    .bare_option("gamma")
                    .into(),
            ),
            (
                "tools".to_string(),
                Question::multi("which?").bare_option("a").bare_option("b").into(),
            ),
            ("yes".to_string(), Question::noul("the statement holds").into()),
        ]
        .into_iter()
        .collect();
        let state = Value::String("hello world of tiny tokens".into());
        let a = candle.decide(HEAD_TURN_LOAD, &state, &questions).unwrap();
        let b = mlx.decide(HEAD_TURN_LOAD, &state, &questions).unwrap();
        for (id, answer) in &a {
            let other = &b[id];
            let pa = answer
                .probabilities
                .clone()
                .or_else(|| {
                    answer.distribution.clone().map(|d| {
                        d.into_iter().enumerate().map(|(i, p)| (i.to_string(), p)).collect()
                    })
                })
                .unwrap_or_default();
            let pb = other
                .probabilities
                .clone()
                .or_else(|| {
                    other.distribution.clone().map(|d| {
                        d.into_iter().enumerate().map(|(i, p)| (i.to_string(), p)).collect()
                    })
                })
                .unwrap_or_default();
            for (label, p) in &pa {
                assert!(
                    (p - pb[label]).abs() < 2e-3,
                    "{id}/{label}: candle {p} vs mlx {}",
                    pb[label]
                );
            }
            if let (Some(x), Some(y)) = (answer.noul, other.noul) {
                assert!((x - y).abs() < 2e-3, "{id}: candle {x} vs mlx {y}");
            }
        }
        let ranked_a = candle
            .rank(
                HEAD_TURN_LOAD,
                "find the config loader",
                &["config.go".into(), "README.md".into()],
            )
            .unwrap();
        let ranked_b = mlx
            .rank(
                HEAD_TURN_LOAD,
                "find the config loader",
                &["config.go".into(), "README.md".into()],
            )
            .unwrap();
        for (x, y) in ranked_a.iter().zip(&ranked_b) {
            assert!((x - y).abs() < 2e-3, "rank: candle {x} vs mlx {y}");
        }
    }
}
