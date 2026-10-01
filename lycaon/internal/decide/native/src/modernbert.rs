//! The ModernBERT backbone, from candle-transformers, with the masks and rotary tables built
//! in the model's own dtype so the encoder can run in half precision on an accelerator.
//!
//! ModernBERT is a modernized bidirectional encoder-only Transformer model
//! ([arXiv:2412.13663](https://arxiv.org/abs/2412.13663), upstream at
//! <https://github.com/AnswerDotAI/ModernBERT>). Only the backbone is kept; the fill-mask and
//! classification heads of the original module are not.

use candle_core::{D, DType, Device, Result, Tensor};
use candle_nn::{
    Embedding, LayerNorm, Linear, Module, VarBuilder, embedding, layer_norm_no_bias,
    linear_no_bias, ops::softmax_last_dim,
};

use std::sync::Arc;

/// Masked attention scores are pushed this far down. Finite, so a fully masked row (a padding
/// query whose whole window is padding) softmaxes to a uniform row rather than NaN, and small
/// enough to survive a cast to f16.
pub(crate) const MASK_FILL: f64 = -1e4;

#[derive(Debug, Clone, PartialEq)]
pub struct Config {
    pub vocab_size: usize,
    pub hidden_size: usize,
    pub num_hidden_layers: usize,
    pub num_attention_heads: usize,
    pub intermediate_size: usize,
    pub max_position_embeddings: usize,
    pub layer_norm_eps: f64,
    pub pad_token_id: u32,
    pub global_attn_every_n_layers: usize,
    pub global_rope_theta: f64,
    pub local_attention: usize,
    pub local_rope_theta: f64,
}

struct RotaryEmbedding {
    sin: Tensor,
    cos: Tensor,
}

impl RotaryEmbedding {
    fn new(dtype: DType, config: &Config, rope_theta: f64, dev: &Device) -> Result<Self> {
        let dim = config.hidden_size / config.num_attention_heads;
        let inv_freq: Vec<_> = (0..dim)
            .step_by(2)
            .map(|i| 1f32 / rope_theta.powf(i as f64 / dim as f64) as f32)
            .collect();
        let inv_freq_len = inv_freq.len();
        let inv_freq = Tensor::from_vec(inv_freq, (1, inv_freq_len), dev)?;
        let max_seq_len = config.max_position_embeddings;
        let t = Tensor::arange(0u32, max_seq_len as u32, dev)?
            .to_dtype(DType::F32)?
            .reshape((max_seq_len, 1))?;
        // Positions times frequencies overflow half precision; the table is built in f32 and
        // only the sines and cosines are cast.
        let freqs = t.matmul(&inv_freq)?;
        Ok(Self { sin: freqs.sin()?.to_dtype(dtype)?, cos: freqs.cos()?.to_dtype(dtype)? })
    }

    fn apply_rotary_emb_qkv(&self, q: &Tensor, k: &Tensor) -> Result<(Tensor, Tensor)> {
        let q_embed = candle_nn::rotary_emb::rope(&q.contiguous()?, &self.cos, &self.sin)?;
        let k_embed = candle_nn::rotary_emb::rope(&k.contiguous()?, &self.cos, &self.sin)?;
        Ok((q_embed, k_embed))
    }
}

#[derive(Clone)]
struct ModernBertAttention {
    qkv: Linear,
    proj: Linear,
    num_attention_heads: usize,
    attention_head_size: usize,
    rotary_emb: Arc<RotaryEmbedding>,
}

impl ModernBertAttention {
    fn load(vb: VarBuilder, config: &Config, rotary_emb: Arc<RotaryEmbedding>) -> Result<Self> {
        let num_attention_heads = config.num_attention_heads;
        let attention_head_size = config.hidden_size / config.num_attention_heads;

        let qkv = linear_no_bias(config.hidden_size, config.hidden_size * 3, vb.pp("Wqkv"))?;
        let proj = linear_no_bias(config.hidden_size, config.hidden_size, vb.pp("Wo"))?;

        Ok(Self { qkv, proj, num_attention_heads, attention_head_size, rotary_emb })
    }

    fn forward(&self, hidden_states: &Tensor, attention_mask: &Tensor) -> Result<Tensor> {
        let xs = hidden_states.clone();
        let (b, seq_len, d) = xs.dims3()?;
        let qkv = xs
            .apply(&self.qkv)?
            .reshape((b, seq_len, 3, self.num_attention_heads, self.attention_head_size))?
            .permute((2, 0, 3, 1, 4))?;

        // Contiguous operands keep every matmul on the fast kernel path.
        let q = qkv.get(0)?.contiguous()?;
        let k = qkv.get(1)?.contiguous()?;
        let v = qkv.get(2)?.contiguous()?;

        let (q, k) = self.rotary_emb.apply_rotary_emb_qkv(&q, &k)?;

        let scale = (self.attention_head_size as f64).powf(-0.5);
        let q = (q * scale)?;

        let att = q.matmul(&k.transpose(D::Minus2, D::Minus1)?.contiguous()?)?;

        let att = att.broadcast_add(attention_mask)?;
        let att = softmax_last_dim(&att)?;

        let xs = att.matmul(&v)?;

        let xs = xs.transpose(1, 2)?.reshape((b, seq_len, d))?;
        let xs = xs.apply(&self.proj)?;
        let xs = xs.reshape((b, seq_len, d))?;

        Ok(xs)
    }
}

#[derive(Clone)]
pub struct ModernBertMLP {
    wi: Linear,
    wo: Linear,
}

impl ModernBertMLP {
    fn load(vb: VarBuilder, config: &Config) -> Result<Self> {
        let wi = linear_no_bias(config.hidden_size, config.intermediate_size * 2, vb.pp("Wi"))?;
        let wo = linear_no_bias(config.intermediate_size, config.hidden_size, vb.pp("Wo"))?;
        Ok(Self { wi, wo })
    }
}

impl Module for ModernBertMLP {
    fn forward(&self, xs: &Tensor) -> Result<Tensor> {
        let xs = xs.apply(&self.wi)?;
        let xs = xs.chunk(2, D::Minus1)?;
        let xs = (&xs[0].contiguous()?.gelu_erf()? * &xs[1].contiguous()?)?.apply(&self.wo)?; // GeGLU
        Ok(xs)
    }
}

#[derive(Clone)]
pub struct ModernBertLayer {
    attn: ModernBertAttention,
    mlp: ModernBertMLP,
    attn_norm: Option<LayerNorm>,
    mlp_norm: LayerNorm,
    uses_local_attention: bool,
}

impl ModernBertLayer {
    fn load(
        vb: VarBuilder,
        config: &Config,
        rotary_emb: Arc<RotaryEmbedding>,
        uses_local_attention: bool,
    ) -> Result<Self> {
        let attn = ModernBertAttention::load(vb.pp("attn"), config, rotary_emb)?;
        let mlp = ModernBertMLP::load(vb.pp("mlp"), config)?;
        let attn_norm =
            layer_norm_no_bias(config.hidden_size, config.layer_norm_eps, vb.pp("attn_norm")).ok();
        let mlp_norm =
            layer_norm_no_bias(config.hidden_size, config.layer_norm_eps, vb.pp("mlp_norm"))?;
        Ok(Self { attn, mlp, attn_norm, mlp_norm, uses_local_attention })
    }

    fn forward(
        &self,
        xs: &Tensor,
        global_attention_mask: &Tensor,
        local_attention_mask: &Tensor,
    ) -> Result<Tensor> {
        let residual = xs.clone();
        let mut xs = xs.clone();
        if let Some(norm) = &self.attn_norm {
            xs = xs.apply(norm)?;
        }

        let attention_mask = if self.uses_local_attention {
            &global_attention_mask.broadcast_add(local_attention_mask)?
        } else {
            global_attention_mask
        };
        let xs = self.attn.forward(&xs, attention_mask)?;
        let xs = (xs + residual)?;
        let mlp_out = xs.apply(&self.mlp_norm)?.apply(&self.mlp)?;
        let xs = (xs + mlp_out)?;
        Ok(xs)
    }
}

// Global attention mask calculated from padded token inputs
fn prepare_4d_attention_mask(
    mask: &Tensor,
    dtype: DType,
    tgt_len: Option<usize>,
) -> Result<Tensor> {
    let bsz = mask.dim(0)?;
    let src_len = mask.dim(1)?;
    let tgt_len = tgt_len.unwrap_or(src_len);

    let expanded_mask =
        mask.unsqueeze(1)?.unsqueeze(2)?.expand((bsz, 1, tgt_len, src_len))?.to_dtype(dtype)?;

    let inverted_mask = (1.0 - expanded_mask)?;

    inverted_mask * MASK_FILL
}

// Attention mask caused by the sliding window
fn get_local_attention_mask(
    seq_len: usize,
    max_distance: usize,
    dtype: DType,
    device: &Device,
) -> Result<Tensor> {
    let mask: Vec<f32> = (0..seq_len)
        .flat_map(|i| {
            (0..seq_len).map(move |j| {
                if (j as i32 - i as i32).abs() > max_distance as i32 {
                    MASK_FILL as f32
                } else {
                    0.
                }
            })
        })
        .collect();
    Tensor::from_slice(&mask, (seq_len, seq_len), device)?.to_dtype(dtype)
}

// ModernBERT backbone
#[derive(Clone)]
pub struct ModernBert {
    word_embeddings: Embedding,
    norm: LayerNorm,
    layers: Vec<ModernBertLayer>,
    final_norm: LayerNorm,
    local_attention_size: usize,
    dtype: DType,
}

impl ModernBert {
    pub fn load(vb: VarBuilder, config: &Config) -> Result<Self> {
        let word_embeddings = embedding(
            config.vocab_size,
            config.hidden_size,
            vb.pp("model.embeddings.tok_embeddings"),
        )?;
        let norm = layer_norm_no_bias(
            config.hidden_size,
            config.layer_norm_eps,
            vb.pp("model.embeddings.norm"),
        )?;
        let global_rotary_emb = Arc::new(RotaryEmbedding::new(
            vb.dtype(),
            config,
            config.global_rope_theta,
            vb.device(),
        )?);
        let local_rotary_emb = Arc::new(RotaryEmbedding::new(
            vb.dtype(),
            config,
            config.local_rope_theta,
            vb.device(),
        )?);

        let mut layers = Vec::with_capacity(config.num_hidden_layers);
        for layer_id in 0..config.num_hidden_layers {
            let layer_uses_local_attention = layer_id % config.global_attn_every_n_layers != 0;
            layers.push(ModernBertLayer::load(
                vb.pp(format!("model.layers.{layer_id}")),
                config,
                if layer_uses_local_attention {
                    local_rotary_emb.clone()
                } else {
                    global_rotary_emb.clone()
                },
                layer_uses_local_attention,
            )?);
        }

        let final_norm = layer_norm_no_bias(
            config.hidden_size,
            config.layer_norm_eps,
            vb.pp("model.final_norm"),
        )?;

        Ok(Self {
            word_embeddings,
            norm,
            layers,
            final_norm,
            local_attention_size: config.local_attention,
            dtype: vb.dtype(),
        })
    }

    pub fn forward(&self, xs: &Tensor, mask: &Tensor) -> Result<Tensor> {
        let seq_len = xs.shape().dims()[1];
        let global_attention_mask =
            prepare_4d_attention_mask(mask, self.dtype, None)?.to_device(xs.device())?;
        let local_attention_mask = get_local_attention_mask(
            seq_len,
            self.local_attention_size / 2,
            self.dtype,
            xs.device(),
        )?;
        let mut xs = xs.apply(&self.word_embeddings)?.apply(&self.norm)?;
        for layer in self.layers.iter() {
            xs = layer.forward(&xs, &global_attention_mask, &local_attention_mask)?;
        }
        let xs = xs.apply(&self.final_norm)?;
        Ok(xs)
    }
}
