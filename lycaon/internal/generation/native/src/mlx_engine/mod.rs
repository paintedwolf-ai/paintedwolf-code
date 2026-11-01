use mlx_rs::{fast, ops, ops::indexing::*, Array};
use std::path::Path;

mod activation;
pub mod backend;
mod cache;
mod config;
mod layout;
mod ple;
mod weights;

use activation::Activation;
pub use cache::Cache;
use ple::MappedPLE;
use weights::{Linear, Weights};

pub type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

pub enum Logits {
    None,
    All,
    Last,
}

fn norm(x: &Array, weight: &Array) -> Result<Array> {
    Ok(fast::rms_norm(x, Some(weight), 1e-6)?)
}

fn scale(x: &Array, value: f32) -> Result<Array> {
    Ok(x.multiply(Array::from_f32(value).as_dtype(x.dtype())?)?)
}

fn rotary(x: &Array, sliding: bool, offset: i32) -> Result<Array> {
    if sliding {
        return Ok(fast::rope(x, 256, false, 10000.0, 1.0, offset, None)?);
    }
    let rotated = ops::concatenate(
        &[x.index((.., .., .., ..64)), x.index((.., .., .., 256..320))],
        -1,
    )?;
    let frequencies = Array::from_slice(
        &(0..64)
            .map(|i| 1_000_000_f32.powf((2 * i) as f32 / 512.0))
            .collect::<Vec<_>>(),
        &[64],
    );
    let rotated = fast::rope(rotated, 128, false, None, 1.0, offset, &frequencies)?;
    Ok(ops::concatenate(
        &[
            rotated.index((.., .., .., ..64)),
            x.index((.., .., .., 64..256)),
            rotated.index((.., .., .., 64..)),
            x.index((.., .., .., 320..)),
        ],
        -1,
    )?)
}

struct Attention {
    q: Linear,
    k: Option<Linear>,
    v: Option<Linear>,
    output: Linear,
    q_norm: Array,
    k_norm: Option<Array>,
}

struct AttentionStep<'a> {
    layer: usize,
    query_offset: i32,
    source_tokens: i32,
    mask: Option<&'a Array>,
}

impl Attention {
    fn load(w: &mut Weights, prefix: &str, layer: usize) -> Result<Self> {
        Ok(Self {
            q: w.linear(&format!("{prefix}.q_proj"))?,
            k: if layer < 24 {
                Some(w.linear(&format!("{prefix}.k_proj"))?)
            } else {
                None
            },
            v: if layer < 24 {
                Some(w.linear(&format!("{prefix}.v_proj"))?)
            } else {
                None
            },
            output: w.linear(&format!("{prefix}.o_proj"))?,
            q_norm: w.take(&format!("{prefix}.q_norm.weight"))?,
            k_norm: if layer < 24 {
                Some(w.take(&format!("{prefix}.k_norm.weight"))?)
            } else {
                None
            },
        })
    }

    fn forward(&self, x: &Array, cache: &mut Cache, step: &AttentionStep<'_>) -> Result<Array> {
        let layer = step.layer;
        let sliding = layer % 6 != 5;
        let dimension = if sliding { 256 } else { 512 };
        let count = x.shape()[1];
        let q = norm(
            &self.q.forward(x)?.reshape(&[1, count, 8, dimension])?,
            &self.q_norm,
        )?;
        let q = rotary(
            &q.transpose_axes(&[0, 2, 1, 3])?,
            sliding,
            step.query_offset,
        )?;
        let kv = if let (Some(k), Some(v), Some(kn)) = (&self.k, &self.v, &self.k_norm) {
            let k = norm(&k.forward(x)?.reshape(&[1, count, 2, dimension])?, kn)?;
            let k = rotary(&k.transpose_axes(&[0, 2, 1, 3])?, sliding, cache.offset)?;
            let v = fast::rms_norm(
                v.forward(x)?.reshape(&[1, count, 2, dimension])?,
                None,
                1e-6,
            )?;
            cache.update(layer, k, v.transpose_axes(&[0, 2, 1, 3])?)?
        } else {
            cache.get(if sliding { 22 } else { 23 }, step.source_tokens)?
        };
        let masking = if sliding {
            step.mask.map(fast::ScaledDotProductAttentionMask::Array)
        } else {
            (count > 1).then_some(fast::ScaledDotProductAttentionMask::Causal)
        };
        let result = fast::scaled_dot_product_attention(q, kv.keys, kv.values, 1.0, masking, None)?;
        self.output
            .forward(
                &result
                    .transpose_axes(&[0, 2, 1, 3])?
                    .reshape(&[1, count, 8 * dimension])?,
            )
    }
}

struct Layer {
    attention: Attention,
    input_norm: Array,
    attention_norm: Array,
    feedforward_norm: Array,
    output_norm: Array,
    gate: Linear,
    up: Linear,
    down: Linear,
    ple_gate: Linear,
    ple_projection: Linear,
    ple_norm: Array,
    scalar: Array,
}

impl Layer {
    fn load(w: &mut Weights, i: usize) -> Result<Self> {
        let prefix = format!("model.layers.{i}");
        Ok(Self {
            attention: Attention::load(w, &format!("{prefix}.self_attn"), i)?,
            input_norm: w.take(&format!("{prefix}.input_layernorm.weight"))?,
            attention_norm: w.take(&format!("{prefix}.post_attention_layernorm.weight"))?,
            feedforward_norm: w.take(&format!("{prefix}.pre_feedforward_layernorm.weight"))?,
            output_norm: w.take(&format!("{prefix}.post_feedforward_layernorm.weight"))?,
            gate: w.linear(&format!("{prefix}.mlp.gate_proj"))?,
            up: w.linear(&format!("{prefix}.mlp.up_proj"))?,
            down: w.linear(&format!("{prefix}.mlp.down_proj"))?,
            ple_gate: w.linear(&format!("{prefix}.per_layer_input_gate"))?,
            ple_projection: w.linear(&format!("{prefix}.per_layer_projection"))?,
            ple_norm: w.take(&format!("{prefix}.post_per_layer_input_norm.weight"))?,
            scalar: w.take(&format!("{prefix}.layer_scalar"))?,
        })
    }

    fn forward(
        &self,
        x: &Array,
        ple: &Array,
        cache: &mut Cache,
        step: &AttentionStep<'_>,
        activation: &Activation,
    ) -> Result<Array> {
        let h = norm(x, &self.input_norm)?;
        let x = x.add(norm(
            &self.attention.forward(&h, cache, step)?,
            &self.attention_norm,
        )?)?;
        let h = norm(&x, &self.feedforward_norm)?;
        let h = activation
            .apply(&self.gate.forward(&h)?)?
            .multiply(self.up.forward(&h)?)?;
        let x = x.add(norm(&self.down.forward(&h)?, &self.output_norm)?)?;
        let h = activation
            .apply(&self.ple_gate.forward(&x)?)?
            .multiply(ple)?;
        Ok(
            x.add(norm(&self.ple_projection.forward(&h)?, &self.ple_norm)?)?
                .multiply(&self.scalar)?,
        )
    }
}

pub struct Model {
    embedding: Linear,
    ple: MappedPLE,
    projection: Linear,
    projection_norm: Array,
    layers: Vec<Layer>,
    final_norm: Array,
    activation: Activation,
}

impl Model {
    pub fn load(directory: &Path) -> Result<Self> {
        let config = config::read(directory)?;
        let quantization = config["quantization"].clone();
        let bits = weights::quantization_bits(&quantization, "model.embed_tokens_per_layer")?;
        let path = directory.join("model.safetensors");
        crate::tensor_inventory::safetensors(&path)?;
        layout::validate(&path, &quantization)?;
        let ple = MappedPLE::open(&path, bits)?;
        let mut weights = Weights::load(&path, quantization)?;
        Ok(Self {
            embedding: weights.linear("model.embed_tokens")?,
            ple,
            projection: weights.linear("model.per_layer_model_projection")?,
            projection_norm: weights.take("model.per_layer_projection_norm.weight")?,
            layers: (0..42)
                .map(|i| Layer::load(&mut weights, i))
                .collect::<Result<_>>()?,
            final_norm: weights.take("model.norm.weight")?,
            activation: Activation::new(),
        })
    }

    pub fn forward(&self, ids: &[i32], cache: &mut Cache, output: Logits) -> Result<Option<Array>> {
        if ids.is_empty()
            || ids.len() > 256
            || ids.iter().any(|id| !(0..262144).contains(id))
            || cache.offset + ids.len() as i32 > 65536
        {
            return Err("E4B input exceeds its bounds".into());
        }
        let count = ids.len() as i32;
        let indices = Array::from_slice(ids, &[1, count]);
        let mut h = scale(&self.embedding.embedding(&indices)?, 2560_f32.sqrt())?;
        let projected = scale(&self.projection.forward(&h)?, 1.0 / 2560_f32.sqrt())?
            .reshape(&[1, count, 42, 256])?;
        let ple = scale(
            &norm(&projected, &self.projection_norm)?.add(scale(&self.ple.gather(ids)?, 16.0)?)?,
            std::f32::consts::FRAC_1_SQRT_2,
        )?;
        let old = cache.offset.min(511);
        let mask = (count > 1).then(|| {
            Array::from_slice(
                &(0..count)
                    .flat_map(|q| (0..old + count).map(move |k| k <= old + q && k > old + q - 512))
                    .collect::<Vec<_>>(),
                &[count, old + count],
            )
        });
        let last_mask = mask.as_ref().map(|mask| mask.index((count - 1..count, ..)));
        for (i, layer) in self.layers.iter().enumerate() {
            if i == 24 && matches!(output, Logits::None) {
                break;
            }
            let last_only = i >= 24 && matches!(output, Logits::Last);
            if i == 24 && last_only {
                // Shared-KV layers need only the query whose logits will be sampled.
                h = h.index((.., count - 1..count, ..));
            }
            let layer_ple = if last_only {
                ple.index((.., count - 1..count, i as i32, ..))
            } else {
                ple.index((.., .., i as i32, ..))
            };
            let step = AttentionStep {
                layer: i,
                query_offset: cache.offset + if last_only { count - 1 } else { 0 },
                source_tokens: count,
                mask: if last_only {
                    last_mask.as_ref()
                } else {
                    mask.as_ref()
                },
            };
            h = layer.forward(&h, &layer_ple, cache, &step, &self.activation)?;
        }
        cache.offset += count;
        if matches!(output, Logits::None) {
            return Ok(None);
        }
        let logits = self.embedding.forward(&norm(&h, &self.final_norm)?)?;
        let cap = Array::from_f32(30.0).as_dtype(logits.dtype())?;
        Ok(Some(ops::tanh(logits.divide(&cap)?)?.multiply(&cap)?))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scalar_scaling_preserves_activation_precision() {
        let input = Array::ones::<f32>(&[1, 2])
            .unwrap()
            .as_dtype(mlx_rs::Dtype::Float16)
            .unwrap();
        let output = scale(&input, 16.0).unwrap();
        assert_eq!(output.dtype(), mlx_rs::Dtype::Float16);
        assert_eq!(output.nbytes(), 4);
    }

    #[test]
    fn proportional_rope_rotates_pairs_across_the_full_head() {
        let source: Vec<f32> = (0..512).map(|x| x as f32 / 512.0).collect();
        let input = Array::from_slice(&source, &[1, 1, 1, 512]);
        let output = rotary(&input, false, 3).unwrap();
        let actual = output.as_slice::<f32>();
        let mut expected = source.clone();
        for i in 0..64 {
            let angle = 3.0 / 1_000_000_f32.powf((2 * i) as f32 / 512.0);
            let (sin, cos) = angle.sin_cos();
            expected[i] = source[i] * cos - source[256 + i] * sin;
            expected[256 + i] = source[256 + i] * cos + source[i] * sin;
        }
        for (i, (&got, &want)) in actual.iter().zip(&expected).enumerate() {
            assert!((got - want).abs() < 1e-5, "coordinate {i}: {got} != {want}");
        }
    }
}
