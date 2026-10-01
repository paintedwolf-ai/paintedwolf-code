//! The decision model: a ModernBERT backbone plus one or more typed decision heads.
//!
//! The head is a port of the training-side module: a type embedding added to every position,
//! two pre-norm transformer layers, and a scorer that reads the `[MASK]` markers. The
//! checkpoints also carry an action head the host never reads, so it is not run. The backbone
//! runs once per batch; the head a request names reads its hidden states.

use std::collections::HashMap;

use crate::modernbert;
use candle_core::{D, DType, Device, Tensor};
use candle_nn::ops::softmax_last_dim;
use candle_nn::{Embedding, LayerNorm, Linear, Module, VarBuilder};

use crate::error::{Error, Result};
use crate::question::QType;

/// The precision a device runs at: half on an accelerator, single on the CPU, where half
/// precision has no fast path.
pub(crate) fn dtype_for(device: &Device) -> DType {
    if device.is_cpu() { DType::F32 } else { DType::F16 }
}

/// Matches the layer-normalization epsilon used to train the heads.
pub(crate) const HEAD_LN_EPS: f64 = 1e-5;
/// Pad positions are pushed this far below the real scores before the softmax; finite so it
/// survives half precision.
pub(crate) const MASKED_SCORE: f64 = -1e4;
/// Markers that do not exist for a given question are scored this low.
pub(crate) const MASKED_OPTION_LOGIT: f32 = -1e4;

/// Multi-head attention over the packed `in_proj_weight` the checkpoint stores.
#[derive(Clone)]
struct MultiheadAttention {
    in_proj: Linear,
    out_proj: Linear,
    n_heads: usize,
    head_dim: usize,
}

impl MultiheadAttention {
    fn load(vb: VarBuilder, hidden: usize, n_heads: usize) -> Result<Self> {
        let w = vb.get((3 * hidden, hidden), "in_proj_weight")?;
        let b = vb.get(3 * hidden, "in_proj_bias")?;
        Ok(Self {
            in_proj: Linear::new(w, Some(b)),
            out_proj: candle_nn::linear(hidden, hidden, vb.pp("out_proj"))?,
            n_heads,
            head_dim: hidden / n_heads,
        })
    }

    /// `bias` is an additive `[B, 1, 1, L]` mask: 0 on real tokens, very negative on padding.
    fn forward(&self, xs: &Tensor, bias: &Tensor) -> Result<Tensor> {
        let (b, l, d) = xs.dims3()?;
        let qkv = self.in_proj.forward(xs)?;
        let split = |offset: usize| -> Result<Tensor> {
            Ok(qkv
                .narrow(2, offset * d, d)?
                .reshape((b, l, self.n_heads, self.head_dim))?
                .transpose(1, 2)?
                .contiguous()?)
        };
        let (q, k, v) = (split(0)?, split(1)?, split(2)?);

        let scale = (self.head_dim as f64).powf(-0.5);
        let att = (q * scale)?.matmul(&k.transpose(D::Minus2, D::Minus1)?)?;
        let att = softmax_last_dim(&att.broadcast_add(bias)?)?;

        let out = att.matmul(&v)?.transpose(1, 2)?.reshape((b, l, d))?;
        Ok(self.out_proj.forward(&out)?)
    }
}

/// A pre-norm transformer encoder layer; the activation is ReLU, as trained.
#[derive(Clone)]
struct HeadLayer {
    self_attn: MultiheadAttention,
    linear1: Linear,
    linear2: Linear,
    norm1: LayerNorm,
    norm2: LayerNorm,
}

impl HeadLayer {
    fn load(vb: VarBuilder, hidden: usize, n_heads: usize) -> Result<Self> {
        let ff = 4 * hidden;
        Ok(Self {
            self_attn: MultiheadAttention::load(vb.pp("self_attn"), hidden, n_heads)?,
            linear1: candle_nn::linear(hidden, ff, vb.pp("linear1"))?,
            linear2: candle_nn::linear(ff, hidden, vb.pp("linear2"))?,
            norm1: candle_nn::layer_norm(hidden, HEAD_LN_EPS, vb.pp("norm1"))?,
            norm2: candle_nn::layer_norm(hidden, HEAD_LN_EPS, vb.pp("norm2"))?,
        })
    }

    fn forward(&self, xs: &Tensor, bias: &Tensor) -> Result<Tensor> {
        let xs = (xs + self.self_attn.forward(&self.norm1.forward(xs)?, bias)?)?;
        let ff = self.linear2.forward(&self.linear1.forward(&self.norm2.forward(&xs)?)?.relu()?)?;
        Ok((xs + ff)?)
    }
}

/// The shape every head over one backbone shares.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct HeadShape {
    pub hidden: usize,
    pub layers: usize,
}

/// One typed decision head: what a training run changes over the frozen backbone.
#[derive(Clone)]
pub(crate) struct Head {
    layers: Vec<HeadLayer>,
    type_emb: Embedding,
    scorer_norm: LayerNorm,
    scorer_fc1: Linear,
    scorer_fc2: Linear,
}

/// The tensor names a head file must carry.
const HEAD_PREFIXES: [&str; 3] = ["head.layers.", "scorer.", "type_emb."];

impl Head {
    /// Build a head from `vb`, in the checkpoint's own `head.layers.*` / `scorer.*` layout.
    fn from_vb(vb: &VarBuilder, shape: HeadShape) -> Result<Self> {
        let hidden = shape.hidden;
        // The training code derives the head's head count from the width, not from the encoder.
        let n_heads = std::cmp::max(1, hidden / 64);
        let layers = (0..shape.layers)
            .map(|i| HeadLayer::load(vb.pp(format!("head.layers.{i}")), hidden, n_heads))
            .collect::<Result<Vec<_>>>()?;
        Ok(Self {
            layers,
            type_emb: candle_nn::embedding(QType::ALL.len(), hidden, vb.pp("type_emb"))?,
            scorer_norm: candle_nn::layer_norm(hidden, HEAD_LN_EPS, vb.pp("scorer.0"))?,
            scorer_fc1: candle_nn::linear(hidden, hidden, vb.pp("scorer.1"))?,
            scorer_fc2: candle_nn::linear(hidden, 1, vb.pp("scorer.3"))?,
        })
    }

    /// Build a head from a head file's tensors.
    pub(crate) fn from_tensors(
        tensors: HashMap<String, Tensor>,
        shape: HeadShape,
        label: &str,
        dtype: DType,
        device: &Device,
    ) -> Result<Self> {
        for prefix in HEAD_PREFIXES {
            if !tensors.keys().any(|k| k.starts_with(prefix)) {
                return Err(Error::Head(format!(
                    "{label}: no {prefix}* tensors, so it is not a decision head"
                )));
            }
        }
        let found = layer_indices(tensors.keys(), "head.layers.");
        if found != (0..shape.layers).collect::<Vec<_>>() {
            return Err(Error::Head(format!(
                "{label}: the backbone's heads have {} layers, but this head has layers {found:?}",
                shape.layers
            )));
        }
        let vb = VarBuilder::from_tensors(tensors, dtype, device);
        Self::from_vb(&vb, shape)
    }
}

/// The layer indices under `prefix` (`"head.layers."` → the `N` of `head.layers.N.*`).
pub(crate) fn layer_indices<'a>(
    keys: impl Iterator<Item = &'a String>,
    prefix: &str,
) -> Vec<usize> {
    let mut found: Vec<usize> =
        keys.filter_map(|k| k.strip_prefix(prefix)?.split('.').next()?.parse().ok()).collect();
    found.sort_unstable();
    found.dedup();
    found
}

/// What one forward pass produces: `[B, K]` per-option logits, with absent options pushed to
/// `MASKED_OPTION_LOGIT`.
pub(crate) type Forward = Vec<Vec<f32>>;

/// The resident backbone and the head the checkpoint shipped with it.
pub(crate) struct DecisionModel {
    encoder: modernbert::ModernBert,
    base: Head,
    shape: HeadShape,
    dtype: DType,
    device: Device,
}

impl DecisionModel {
    /// Build the model from a checkpoint's tensors.
    ///
    /// `weights` is the raw `model.safetensors` map; encoder keys are rewritten from
    /// `encoder.*` to the `encoder.model.*` layout candle's ModernBERT expects.
    pub(crate) fn load(
        weights: HashMap<String, Tensor>,
        enc_cfg: &modernbert::Config,
        head_layers: usize,
        dtype: DType,
        device: &Device,
    ) -> Result<Self> {
        let remapped: HashMap<String, Tensor> = weights
            .into_iter()
            .map(|(k, v)| match k.strip_prefix("encoder.") {
                Some(rest) => (format!("encoder.model.{rest}"), v),
                None => (k, v),
            })
            .collect();
        Self::from_vb(
            VarBuilder::from_tensors(remapped, dtype, device),
            enc_cfg,
            head_layers,
            device,
        )
    }

    /// Build the model from whatever `vb` holds, in the in-memory `encoder.model.*` layout.
    fn from_vb(
        vb: VarBuilder,
        enc_cfg: &modernbert::Config,
        head_layers: usize,
        device: &Device,
    ) -> Result<Self> {
        let shape = HeadShape { hidden: enc_cfg.hidden_size, layers: head_layers };
        let encoder = modernbert::ModernBert::load(vb.pp("encoder"), enc_cfg)?;
        let base = Head::from_vb(&vb, shape)?;
        Ok(Self { encoder, base, shape, dtype: vb.dtype(), device: device.clone() })
    }

    pub(crate) fn shape(&self) -> HeadShape {
        self.shape
    }

    pub(crate) fn dtype(&self) -> DType {
        self.dtype
    }

    pub(crate) fn device(&self) -> &Device {
        &self.device
    }

    /// The head the checkpoint shipped.
    pub(crate) fn base_head(&self) -> &Head {
        &self.base
    }

    /// Run the backbone and `head` over one collated batch.
    pub(crate) fn forward(&self, head: &Head, batch: &crate::sequence::Batch) -> Result<Forward> {
        let dev = &self.device;
        let (b, l, k) = (batch.batch, batch.seq_len, batch.k_max);

        let input_ids = Tensor::from_slice(&batch.input_ids, (b, l), dev)?;
        let attention_mask = Tensor::from_slice(&batch.attention_mask, (b, l), dev)?;
        let marker_pos = Tensor::from_slice(&batch.marker_pos, (b, k), dev)?;
        let marker_mask = Tensor::from_slice(&batch.marker_mask, (b, k), dev)?;
        let qtype = Tensor::from_slice(&batch.qtype, b, dev)?;

        let hs = self.encoder.forward(&input_ids, &attention_mask)?;

        // The question type is a global signal, so it is added to every position.
        let type_vec = head.type_emb.forward(&qtype)?.unsqueeze(1)?;
        let mut hs = hs.broadcast_add(&type_vec)?;

        // Padding is masked out of the head's attention exactly as `src_key_padding_mask` does.
        let bias = ((1.0 - &attention_mask)? * MASKED_SCORE)?
            .reshape((b, 1, 1, l))?
            .to_dtype(self.dtype)?;
        for layer in &head.layers {
            hs = layer.forward(&hs, &bias)?;
        }

        // One hidden state per marker, then a scalar score per option.
        let hidden = self.shape.hidden;
        let idx = marker_pos.unsqueeze(2)?.expand((b, k, hidden))?.contiguous()?;
        let markers = hs.gather(&idx, 1)?;
        let logits = head
            .scorer_fc2
            .forward(&head.scorer_fc1.forward(&head.scorer_norm.forward(&markers)?)?.gelu_erf()?)?
            .squeeze(2)?;

        let logits = logits.to_dtype(DType::F32)?;
        let masked = Tensor::full(MASKED_OPTION_LOGIT, (b, k), dev)?;
        let logits = marker_mask.where_cond(&logits, &masked)?;
        Ok(logits.to_vec2::<f32>()?)
    }
}

/// Randomly initialised tensors for a model of this shape, keyed as `model.safetensors` stores
/// them, so tests can load a real (if tiny) checkpoint without downloading one.
#[cfg(test)]
pub(crate) fn random_weights(
    enc_cfg: &modernbert::Config,
    head_layers: usize,
) -> Result<HashMap<String, Tensor>> {
    let varmap = candle_nn::VarMap::new();
    let vb = VarBuilder::from_varmap(&varmap, DType::F32, &Device::Cpu);
    DecisionModel::from_vb(vb.clone(), enc_cfg, head_layers, &Device::Cpu)?;
    // Checkpoint validation includes the unused auxiliary action head.
    candle_nn::linear(enc_cfg.hidden_size + 4, 256, vb.pp("act_head.0"))?;
    candle_nn::linear(256, 2, vb.pp("act_head.2"))?;
    let tensors = varmap.data().lock().expect("no other thread holds the VarMap");
    Ok(tensors
        .iter()
        .map(|(k, v)| {
            let key = match k.strip_prefix("encoder.model.") {
                Some(rest) => format!("encoder.{rest}"),
                None => k.clone(),
            };
            (key, v.as_tensor().clone())
        })
        .collect())
}

/// The head tensors out of a full weight map: what a head file carries.
#[cfg(test)]
pub(crate) fn head_tensors(weights: &HashMap<String, Tensor>) -> HashMap<String, Tensor> {
    weights
        .iter()
        .filter(|(k, _)| HEAD_PREFIXES.iter().any(|p| k.starts_with(p)))
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect()
}
