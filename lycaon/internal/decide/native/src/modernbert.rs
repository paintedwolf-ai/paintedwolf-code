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
        let gated = if xs.device().is_cpu() && xs.dtype() == DType::F32 {
            xs.contiguous()?.apply_op1_no_bwd(&CpuGeGlu)?
        } else {
            let halves = xs.chunk(2, D::Minus1)?;
            (&halves[0].contiguous()?.gelu_erf()? * &halves[1].contiguous()?)?
        };
        gated.apply(&self.wo) // GeGLU
    }
}

/// ModernBERT's bias-free layer norm. On CPU in f32 the element-wise steps run across all
/// threads with the exact arithmetic of candle's bias-free path, whose centre, divide and
/// scale operators each ran on a single thread; the two row sums stay candle's own.
fn normalize(norm: &LayerNorm, xs: &Tensor) -> Result<Tensor> {
    if !(norm.bias().is_none() && norm.remove_mean() && xs.device().is_cpu() && xs.dtype() == DType::F32) {
        return xs.apply(norm);
    }
    let xs = xs.contiguous()?;
    let inverse = (1. / xs.dim(D::Minus1)? as f64) as f32;
    let centered = xs.apply_op2_no_bwd(&xs.sum_keepdim(D::Minus1)?, &CpuCentre { inverse })?;
    let variance = centered.sqr()?.sum_keepdim(D::Minus1)?;
    centered.apply_op3_no_bwd(&variance, norm.weight(), &CpuScale { inverse, eps: norm.eps() as f32 })
}

/// Contiguous f32 values of a CPU tensor.
fn f32_values<'a>(storage: &'a candle_core::CpuStorage, layout: &candle_core::Layout) -> Result<&'a [f32]> {
    match (storage, layout.contiguous_offsets()) {
        (candle_core::CpuStorage::F32(values), Some((start, end))) => Ok(&values[start..end]),
        _ => candle_core::bail!("the fused CPU layer norm needs contiguous f32 operands"),
    }
}

/// `x - sum / width` per row, as candle's affine mean and broadcast subtraction compute it.
struct CpuCentre {
    inverse: f32,
}

impl candle_core::CustomOp2 for CpuCentre {
    fn name(&self) -> &'static str {
        "cpu-layer-norm-centre"
    }

    fn cpu_fwd(
        &self,
        storage: &candle_core::CpuStorage,
        layout: &candle_core::Layout,
        sums: &candle_core::CpuStorage,
        sums_layout: &candle_core::Layout,
    ) -> Result<(candle_core::CpuStorage, candle_core::Shape)> {
        use rayon::prelude::*;
        let (values, sums) = (f32_values(storage, layout)?, f32_values(sums, sums_layout)?);
        let width = *layout.shape().dims().last().unwrap_or(&0);
        if width == 0 || values.len() != sums.len() * width {
            candle_core::bail!("cpu-layer-norm-centre needs one sum per row");
        }
        let mut centered = vec![0f32; values.len()];
        centered.par_chunks_mut(width).zip(values.par_chunks(width)).zip(sums.par_iter()).for_each(
            |((out, row), &sum)| {
                let mean = sum * self.inverse + 0.;
                for (out, &x) in out.iter_mut().zip(row) {
                    *out = x - mean;
                }
            },
        );
        Ok((candle_core::CpuStorage::F32(centered), layout.shape().clone()))
    }
}

/// `centered / sqrt(variance + eps) * weight` per row, as candle's affine, broadcast divide
/// and broadcast multiply compute it.
struct CpuScale {
    inverse: f32,
    eps: f32,
}

impl candle_core::CustomOp3 for CpuScale {
    fn name(&self) -> &'static str {
        "cpu-layer-norm-scale"
    }

    fn cpu_fwd(
        &self,
        storage: &candle_core::CpuStorage,
        layout: &candle_core::Layout,
        variance: &candle_core::CpuStorage,
        variance_layout: &candle_core::Layout,
        weight: &candle_core::CpuStorage,
        weight_layout: &candle_core::Layout,
    ) -> Result<(candle_core::CpuStorage, candle_core::Shape)> {
        use rayon::prelude::*;
        let values = f32_values(storage, layout)?;
        let (variance, weight) = (f32_values(variance, variance_layout)?, f32_values(weight, weight_layout)?);
        let width = weight.len();
        if width == 0 || values.len() != variance.len() * width {
            candle_core::bail!("cpu-layer-norm-scale needs one variance per row and one weight per column");
        }
        let mut scaled = vec![0f32; values.len()];
        scaled.par_chunks_mut(width).zip(values.par_chunks(width)).zip(variance.par_iter()).for_each(
            |((out, row), &variance)| {
                let deviation = ((variance * self.inverse + 0.) * 1. + self.eps).sqrt();
                for ((out, &x), &weight) in out.iter_mut().zip(row).zip(weight) {
                    *out = x / deviation * weight;
                }
            },
        );
        Ok((candle_core::CpuStorage::F32(scaled), layout.shape().clone()))
    }
}

/// GeGLU over the last dimension, `gelu_erf(x[..n]) * x[n..]`, in one pass over all CPU
/// threads. candle's element-wise CPU operators run on a single thread, which left the
/// activation and its two half copies as the serial share of every encoder layer. The
/// arithmetic matches candle's `gelu_erf` and multiply exactly.
struct CpuGeGlu;

impl candle_core::CustomOp1 for CpuGeGlu {
    fn name(&self) -> &'static str {
        "cpu-geglu"
    }

    fn cpu_fwd(
        &self,
        storage: &candle_core::CpuStorage,
        layout: &candle_core::Layout,
    ) -> Result<(candle_core::CpuStorage, candle_core::Shape)> {
        use rayon::prelude::*;
        let (candle_core::CpuStorage::F32(values), Some((start, end))) =
            (storage, layout.contiguous_offsets())
        else {
            candle_core::bail!("cpu-geglu needs a contiguous f32 tensor");
        };
        let dims = layout.shape().dims();
        let width = *dims.last().unwrap_or(&0);
        if width == 0 || width % 2 != 0 {
            candle_core::bail!("cpu-geglu needs an even last dimension, got {width}");
        }
        let half = width / 2;
        let mut gated = vec![0f32; (end - start) / 2];
        gated.par_chunks_mut(half).zip(values[start..end].par_chunks(width)).for_each(|(out, row)| {
            let (x, gate) = row.split_at(half);
            for ((out, &x), &gate) in out.iter_mut().zip(x).zip(gate) {
                let gelu =
                    (candle_core::cpu::erf::erf_f32(x * std::f32::consts::FRAC_1_SQRT_2) + 1.) * 0.5 * x;
                *out = gelu * gate;
            }
        });
        let mut shape = dims.to_vec();
        *shape.last_mut().unwrap() = half;
        Ok((candle_core::CpuStorage::F32(gated), candle_core::Shape::from(shape)))
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
            xs = normalize(norm, &xs)?;
        }

        let attention_mask = if self.uses_local_attention {
            &global_attention_mask.broadcast_add(local_attention_mask)?
        } else {
            global_attention_mask
        };
        let xs = self.attn.forward(&xs, attention_mask)?;
        let xs = (xs + residual)?;
        let mlp_out = normalize(&self.mlp_norm, &xs)?.apply(&self.mlp)?;
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
        let mut xs = normalize(&self.norm, &xs.apply(&self.word_embeddings)?)?;
        for layer in self.layers.iter() {
            xs = layer.forward(&xs, &global_attention_mask, &local_attention_mask)?;
        }
        let xs = normalize(&self.final_norm, &xs)?;
        Ok(xs)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The parallel CPU GeGLU returns exactly what candle's operators compute.
    #[test]
    fn cpu_geglu_matches_candles_gelu_erf_and_multiply_bit_for_bit() {
        let values: Vec<f32> = (0..3 * 5 * 2 * 37).map(|i| ((i * 7919) % 2003) as f32 / 211.0 - 4.7).collect();
        let xs = Tensor::from_vec(values, (3, 5, 74), &Device::Cpu).unwrap();
        let halves = xs.chunk(2, D::Minus1).unwrap();
        let reference = (&halves[0].contiguous().unwrap().gelu_erf().unwrap() * &halves[1].contiguous().unwrap())
            .unwrap()
            .flatten_all()
            .unwrap()
            .to_vec1::<f32>()
            .unwrap();
        let gated = xs.apply_op1_no_bwd(&CpuGeGlu).unwrap();
        assert_eq!(gated.dims(), &[3, 5, 37]);
        let gated = gated.flatten_all().unwrap().to_vec1::<f32>().unwrap();
        assert!(reference.iter().zip(&gated).all(|(a, b)| a.to_bits() == b.to_bits()));
    }

    /// The parallel CPU layer norm returns exactly what candle's bias-free path computes.
    #[test]
    fn cpu_layer_norm_matches_candles_bias_free_layer_norm_bit_for_bit() {
        let width = 768;
        let values: Vec<f32> = (0..7 * width).map(|i| ((i * 104729) % 4099) as f32 / 97.0 - 21.0).collect();
        let xs = Tensor::from_vec(values, (7, width), &Device::Cpu).unwrap();
        let weight: Vec<f32> = (0..width).map(|i| 0.5 + (i % 13) as f32 / 7.0).collect();
        let norm = LayerNorm::new_no_bias(Tensor::from_vec(weight, width, &Device::Cpu).unwrap(), 1e-5);
        let reference = xs.apply(&norm).unwrap().flatten_all().unwrap().to_vec1::<f32>().unwrap();
        let fused = normalize(&norm, &xs).unwrap().flatten_all().unwrap().to_vec1::<f32>().unwrap();
        assert!(reference.iter().zip(&fused).all(|(a, b)| a.to_bits() == b.to_bits()));
    }

    #[test]
    fn cpu_geglu_refuses_odd_or_strided_input() {
        let odd = Tensor::zeros((2, 3), DType::F32, &Device::Cpu).unwrap();
        assert!(odd.apply_op1_no_bwd(&CpuGeGlu).is_err());
        let strided = Tensor::zeros((4, 2), DType::F32, &Device::Cpu).unwrap().t().unwrap();
        assert!(strided.apply_op1_no_bwd(&CpuGeGlu).is_err());
    }
}
