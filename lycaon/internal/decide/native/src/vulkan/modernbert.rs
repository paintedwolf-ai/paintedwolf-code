//! The ModernBERT backbone on the Vulkan runtime, op for op with the candle port in
//! `crate::modernbert`.

use burn::tensor::{DType, Device, Int, Tensor, TensorData, activation, module};

use super::layers::{LayerNorm, Linear, Weights, heads, merge, padding_bias};
use crate::error::Result;
use crate::modernbert::{Config, MASK_FILL};

/// Non-interleaved rotation: the second half of each head dimension pairs with the first.
fn rope(x: Tensor<4>, table: &Rotary) -> Tensor<4> {
    let halves = x.clone().chunk(2, 3);
    let rotated = Tensor::cat(vec![halves[1].clone().neg(), halves[0].clone()], 3);
    x * table.cos.clone() + rotated * table.sin.clone()
}

/// Sines and cosines for one sequence length, `[1, 1, L, D]`.
struct Rotary {
    cos: Tensor<4>,
    sin: Tensor<4>,
}

impl Rotary {
    /// Positions times frequencies overflow half precision, so the table is built in f32 and
    /// only the sines and cosines are cast.
    fn new(theta: f64, head_dim: usize, len: usize, device: &Device, dtype: DType) -> Self {
        let half = head_dim / 2;
        let inv: Vec<f32> =
            (0..half).map(|i| 1f32 / theta.powf((2 * i) as f64 / head_dim as f64) as f32).collect();
        let mut cos = Vec::with_capacity(len * head_dim);
        let mut sin = Vec::with_capacity(len * head_dim);
        for p in 0..len {
            for j in 0..head_dim {
                let f = p as f32 * inv[j % half];
                cos.push(f.cos());
                sin.push(f.sin());
            }
        }
        let table = |v: Vec<f32>| {
            Tensor::<4>::from_data(TensorData::new(v, [1, 1, len, head_dim]), (device, DType::F32))
                .cast(dtype)
        };
        Self { cos: table(cos), sin: table(sin) }
    }
}

/// `[1, 1, L, L]` with `MASK_FILL` wherever two positions are more than `window` apart.
fn window_bias(l: usize, window: usize, device: &Device, dtype: DType) -> Tensor<4> {
    let mut values = Vec::with_capacity(l * l);
    for i in 0..l {
        for j in 0..l {
            values.push(if i.abs_diff(j) > window { MASK_FILL as f32 } else { 0.0 });
        }
    }
    Tensor::<4>::from_data(TensorData::new(values, [1, 1, l, l]), (device, DType::F32)).cast(dtype)
}

struct Layer {
    attn_norm: Option<LayerNorm>,
    qkv: Linear,
    proj: Linear,
    mlp_norm: LayerNorm,
    wi: Linear,
    wo: Linear,
    local: bool,
}

pub(super) struct Backbone {
    tok: Tensor<2>,
    norm: LayerNorm,
    layers: Vec<Layer>,
    final_norm: LayerNorm,
    heads: usize,
    head_dim: usize,
    window: usize,
    local_theta: f64,
    global_theta: f64,
}

impl Backbone {
    pub fn load(weights: &mut Weights, cfg: &Config) -> Result<Self> {
        let eps = cfg.layer_norm_eps;
        let mut layers = Vec::with_capacity(cfg.num_hidden_layers);
        for i in 0..cfg.num_hidden_layers {
            let p = format!("encoder.layers.{i}");
            let attn_norm = if weights.has(&format!("{p}.attn_norm.weight")) {
                Some(LayerNorm::load(weights, &format!("{p}.attn_norm"), false, eps)?)
            } else {
                None
            };
            layers.push(Layer {
                attn_norm,
                qkv: Linear::load(weights, &format!("{p}.attn.Wqkv"), false)?,
                proj: Linear::load(weights, &format!("{p}.attn.Wo"), false)?,
                mlp_norm: LayerNorm::load(weights, &format!("{p}.mlp_norm"), false, eps)?,
                wi: Linear::load(weights, &format!("{p}.mlp.Wi"), false)?,
                wo: Linear::load(weights, &format!("{p}.mlp.Wo"), false)?,
                local: i % cfg.global_attn_every_n_layers != 0,
            });
        }
        Ok(Self {
            tok: weights.take("encoder.embeddings.tok_embeddings.weight")?,
            norm: LayerNorm::load(weights, "encoder.embeddings.norm", false, eps)?,
            layers,
            final_norm: LayerNorm::load(weights, "encoder.final_norm", false, eps)?,
            heads: cfg.num_attention_heads,
            head_dim: cfg.hidden_size / cfg.num_attention_heads,
            window: cfg.local_attention / 2,
            local_theta: cfg.local_rope_theta,
            global_theta: cfg.global_rope_theta,
        })
    }

    /// `[B, L]` token ids and a `[B, L]` 0/1 mask to `[B, L, D]` hidden states.
    pub fn forward(&self, ids: Tensor<2, Int>, mask: &Tensor<2>, dtype: DType) -> Tensor<3> {
        let device = ids.device();
        let [b, l] = ids.dims();
        let global = padding_bias(mask, MASK_FILL, dtype).expand([b, self.heads, l, l]);
        let local = global.clone() + window_bias(l, self.window, &device, dtype);
        let local_rope = Rotary::new(self.local_theta, self.head_dim, l, &device, dtype);
        let global_rope = Rotary::new(self.global_theta, self.head_dim, l, &device, dtype);

        let mut x = self.norm.forward(module::embedding(self.tok.clone(), ids));
        for layer in &self.layers {
            let h = match &layer.attn_norm {
                Some(norm) => norm.forward(x.clone()),
                None => x.clone(),
            };
            let (bias, table) =
                if layer.local { (&local, &local_rope) } else { (&global, &global_rope) };
            let parts = layer.qkv.forward(h).chunk(3, 2);
            let q = rope(heads(parts[0].clone(), self.heads), table);
            let k = rope(heads(parts[1].clone(), self.heads), table);
            let v = heads(parts[2].clone(), self.heads);
            let att = module::attention(q, k, v, None, Some(bias.clone()), Default::default());
            x = x + layer.proj.forward(merge(att));
            let m = layer.wi.forward(layer.mlp_norm.forward(x.clone())).chunk(2, 2);
            x = x + layer.wo.forward(activation::gelu(m[0].clone()) * m[1].clone());
        }
        self.final_norm.forward(x)
    }
}
