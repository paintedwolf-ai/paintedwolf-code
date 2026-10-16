//! The decision model on a Vulkan GPU: the backbone plus typed decision heads, op for op with
//! the candle port in `crate::model`, so a checkpoint answers the same on either runtime up to
//! rounding; the parity tests check that.

use std::collections::HashMap;

use burn::tensor::{Bool, DType, Int, Tensor, TensorData, activation, module};
use candle_core::Tensor as CandleTensor;

use super::layers::{LayerNorm, Linear, Weights, heads, merge, padding_bias};
use super::modernbert::Backbone;
use super::{Gpu, guard};
use crate::error::{Error, Result};
use crate::model::{HEAD_LN_EPS, HeadShape, MASKED_OPTION_LOGIT, MASKED_SCORE, layer_indices};
use crate::modernbert::Config;
use crate::sequence::Batch;

#[derive(Clone)]
struct HeadLayer {
    norm1: LayerNorm,
    in_proj: Linear,
    out_proj: Linear,
    norm2: LayerNorm,
    linear1: Linear,
    linear2: Linear,
    heads: usize,
}

impl HeadLayer {
    fn load(weights: &mut Weights, prefix: &str, hidden: usize) -> Result<Self> {
        Ok(Self {
            norm1: LayerNorm::load(weights, &format!("{prefix}.norm1"), true, HEAD_LN_EPS)?,
            in_proj: Linear::from_parts(
                weights,
                &format!("{prefix}.self_attn.in_proj_weight"),
                Some(format!("{prefix}.self_attn.in_proj_bias")),
            )?,
            out_proj: Linear::load(weights, &format!("{prefix}.self_attn.out_proj"), true)?,
            norm2: LayerNorm::load(weights, &format!("{prefix}.norm2"), true, HEAD_LN_EPS)?,
            linear1: Linear::load(weights, &format!("{prefix}.linear1"), true)?,
            linear2: Linear::load(weights, &format!("{prefix}.linear2"), true)?,
            // The training code derives the head count from the width, not from the encoder.
            heads: std::cmp::max(1, hidden / 64),
        })
    }

    fn forward(&self, x: Tensor<3>, bias: &Tensor<4>) -> Tensor<3> {
        let parts = self.in_proj.forward(self.norm1.forward(x.clone())).chunk(3, 2);
        let att = module::attention(
            heads(parts[0].clone(), self.heads),
            heads(parts[1].clone(), self.heads),
            heads(parts[2].clone(), self.heads),
            None,
            Some(bias.clone()),
            Default::default(),
        );
        let x = x + self.out_proj.forward(merge(att));
        let ff = self.linear2.forward(activation::relu(self.linear1.forward(self.norm2.forward(x.clone()))));
        x + ff
    }
}

/// One typed decision head on the Vulkan runtime.
#[derive(Clone)]
pub(crate) struct Head {
    layers: Vec<HeadLayer>,
    type_emb: Tensor<2>,
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
            scorer_norm: LayerNorm::load(weights, "scorer.0", true, HEAD_LN_EPS)?,
            scorer_fc1: Linear::load(weights, "scorer.1", true)?,
            scorer_fc2: Linear::load(weights, "scorer.3", true)?,
        })
    }

    /// Build a head from a head file's tensors.
    pub(crate) fn from_tensors(
        tensors: HashMap<String, CandleTensor>,
        shape: HeadShape,
        label: &str,
        model: &Model,
    ) -> Result<Self> {
        for prefix in ["head.layers.", "scorer.", "type_emb."] {
            if !tensors.keys().any(|k| k.starts_with(prefix)) {
                return Err(Error::Head(format!(
                    "{label}: no {prefix}* tensors, so it is not a decision head"
                )));
            }
        }
        guard("loading a head", || {
            let mut weights = Weights {
                tensors,
                device: &model.gpu.device,
                dtype: model.dtype,
                label: label.to_string(),
            };
            Self::load(&mut weights, shape)
        })
    }
}

/// The resident backbone and the checkpoint's own head, on one Vulkan GPU.
pub(crate) struct Model {
    backbone: Backbone,
    base: Head,
    shape: HeadShape,
    dtype: DType,
    gpu: Gpu,
}

impl Model {
    /// Upload `model.safetensors` (already verified by name) onto `gpu`.
    pub(crate) fn load(
        weights: HashMap<String, CandleTensor>,
        enc_cfg: &Config,
        head_layers: usize,
        dtype: DType,
        gpu: Gpu,
        label: &str,
    ) -> Result<Self> {
        guard("loading the checkpoint", || {
            let shape = HeadShape { hidden: enc_cfg.hidden_size, layers: head_layers };
            let mut weights =
                Weights { tensors: weights, device: &gpu.device, dtype, label: label.to_string() };
            let backbone = Backbone::load(&mut weights, enc_cfg)?;
            let base = Head::load(&mut weights, shape)?;
            Ok(Self { backbone, base, shape, dtype, gpu: gpu.clone() })
        })
    }

    pub(crate) fn shape(&self) -> HeadShape {
        self.shape
    }

    pub(crate) fn dtype(&self) -> DType {
        self.dtype
    }

    pub(crate) fn base_head(&self) -> &Head {
        &self.base
    }

    /// Run the backbone and `head` over one collated batch: `[B, K]` logits per row.
    pub(crate) fn forward(&self, head: &Head, batch: &Batch) -> Result<crate::model::Forward> {
        guard("forward pass", || self.forward_unguarded(head, batch))
    }

    fn forward_unguarded(&self, head: &Head, batch: &Batch) -> Result<crate::model::Forward> {
        let device = &self.gpu.device;
        let (b, l, k) = (batch.batch, batch.seq_len, batch.k_max);
        let ints = |v: &[u32], shape: Vec<usize>| {
            let v: Vec<i32> = v.iter().map(|&x| x as i32).collect();
            TensorData::new(v, shape)
        };
        let ids = Tensor::<2, Int>::from_data(ints(&batch.input_ids, vec![b, l]), (device, DType::I32));
        let mask = Tensor::<2>::from_data(
            TensorData::new(batch.attention_mask.clone(), [b, l]),
            (device, DType::F32),
        );
        let marker_pos =
            Tensor::<2, Int>::from_data(ints(&batch.marker_pos, vec![b, k]), (device, DType::I32));
        let present: Vec<bool> = batch.marker_mask.iter().map(|&m| m != 0).collect();
        let marker_mask = Tensor::<2, Bool>::from_data(TensorData::new(present, [b, k]), device);
        let qtype = Tensor::<1, Int>::from_data(ints(&batch.qtype, vec![b]), (device, DType::I32));

        let hs = self.backbone.forward(ids, &mask, self.dtype);
        let hidden = self.shape.hidden;
        // The question type is a global signal, so it is added to every position.
        let mut hs = hs + head.type_emb.clone().select(0, qtype).unsqueeze_dim::<3>(1);
        // Padding is masked out of the head's attention exactly as `src_key_padding_mask` does.
        let heads = head.layers.first().map_or(1, |layer| layer.heads);
        let bias = padding_bias(&mask, MASKED_SCORE, self.dtype).expand([b, heads, l, l]);
        for layer in &head.layers {
            hs = layer.forward(hs, &bias);
        }
        // One hidden state per marker, then a scalar score per option.
        let idx = marker_pos.unsqueeze_dim::<3>(2).expand([b, k, hidden]);
        let markers = hs.gather(1, idx);
        let scored = head.scorer_fc2.forward(activation::gelu(
            head.scorer_fc1.forward(head.scorer_norm.forward(markers)),
        ));
        let logits: Tensor<2> = scored.squeeze_dim::<2>(2).cast(DType::F32);
        let logits = logits.mask_fill(marker_mask.bool_not(), MASKED_OPTION_LOGIT);
        let flat = logits
            .try_into_data()
            .map_err(|e| Error::Device(format!("vulkan: reading logits: {e:?}")))?
            .try_to_vec::<f32>()
            .map_err(|e| Error::Device(format!("vulkan: reading logits: {e:?}")))?;
        Ok(flat.chunks(k).map(|row| row.to_vec()).collect())
    }
}

#[cfg(test)]
mod tests {
    use std::path::Path;

    use crate::device::Selected;
    use crate::engine::{Engine, EngineOptions, HEAD_CODE_RANK, Prepared};
    use crate::testutil::{assert_agrees_with_cpu, scratch_dir, write_tiny_checkpoint, write_tiny_head};

    fn engine_on_test_gpu(dir: &Path, head: &Path, dtype: &str) -> Engine {
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            dtype: dtype.into(),
            heads: vec![(HEAD_CODE_RANK.to_string(), head.to_path_buf())],
            ..Default::default()
        };
        let prepared = Prepared::read(dir).unwrap();
        Engine::on_selected(&prepared, Selected::Vulkan(super::super::test_gpu()), &opts).unwrap()
    }

    #[test]
    fn vulkan_matches_the_cpu_on_the_same_checkpoint() {
        let dir = scratch_dir("vulkan-parity");
        write_tiny_checkpoint(&dir);
        let head = write_tiny_head(&dir, "rank", "tiny");
        let engine = engine_on_test_gpu(&dir, &head, "f32");
        assert!(engine.identity().device.starts_with("vulkan:"), "{:?}", engine.identity());
        assert!(!engine.identity().adapter.is_empty());
        assert_eq!(engine.identity().dtype, "f32");
        assert_agrees_with_cpu(&dir, &head, &engine, 2e-3);
    }

    #[test]
    fn the_gpu_precision_stays_close_to_the_cpu() {
        let dir = scratch_dir("vulkan-native-precision");
        write_tiny_checkpoint(&dir);
        let head = write_tiny_head(&dir, "rank", "tiny");
        let engine = engine_on_test_gpu(&dir, &head, "");
        let tolerance = if engine.identity().dtype == "f16" { 3e-2 } else { 2e-3 };
        assert_agrees_with_cpu(&dir, &head, &engine, tolerance);
    }
}
