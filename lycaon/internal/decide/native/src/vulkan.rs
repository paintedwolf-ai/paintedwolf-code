//! The decision model on any Vulkan GPU, through Burn (`--device vulkan:N`, a candidate for
//! `auto`). It mirrors the candle port in `modernbert.rs` and `model.rs` op for op, so a
//! checkpoint answers the same on either runtime up to rounding; the parity test checks that.
//!
//! Burn panics on device faults rather than returning errors, so loading and every forward
//! pass run under [`guard`], which turns a panic into [`Error::Device`]. A faulty GPU then
//! costs its candidacy, not the engine.

use std::collections::HashMap;
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::Path;
use std::sync::OnceLock;

use burn::cubecl::config::cache::CacheConfig;
use burn::cubecl::config::{CubeClRuntimeConfig, RuntimeConfig};
use burn::tensor::{
    Bool, DType, Device, DeviceType, Int, Tensor, TensorData, activation, module,
};
use candle_core::Tensor as CandleTensor;

use crate::error::{Error, Result};
use crate::model::{HEAD_LN_EPS, HeadShape, MASKED_OPTION_LOGIT, MASKED_SCORE, layer_indices};
use crate::modernbert::{Config, MASK_FILL};
use crate::sequence::Batch;

/// One GPU Vulkan reports, with what identifies it across launches.
#[derive(Clone)]
pub(crate) struct Gpu {
    pub index: usize,
    /// The name the driver gives the part.
    pub name: String,
    /// The part, its kernel target, and its bus address: a new card, driver target, or slot is
    /// new hardware to measure.
    pub key: String,
    device: Device,
}

/// Point the kernel and autotune caches at `dir`, or keep them in memory when there is none.
/// Takes effect only before the first device opens.
pub(crate) fn configure_cache(dir: Option<&Path>) {
    let mut config = CubeClRuntimeConfig::default();
    match dir {
        Some(dir) => {
            config.environment.path = CacheConfig::Directory(dir.join("kernels"));
            config.compilation.cache = true;
        }
        None => config.autotune.disable_cache = true,
    }
    CubeClRuntimeConfig::try_set(config);
}

/// The GPUs Vulkan reports, in its order. Software rasterizers are left out: they are no card,
/// and the CPU runtime serves a CPU better.
pub(crate) fn gpus() -> &'static [Gpu] {
    static GPUS: OnceLock<Vec<Gpu>> = OnceLock::new();
    GPUS.get_or_init(|| guard("enumerating Vulkan GPUs", || enumerate(false)).unwrap_or_default())
}

fn enumerate(include_software: bool) -> Result<Vec<Gpu>> {
    let mut gpus = Vec::new();
    for device in Device::enumerate(DeviceType::Vulkan) {
        let Some(identity) = device.identity() else { continue };
        if identity.physical.is_none() && !include_software {
            continue;
        }
        let address = identity
            .physical
            .and_then(|p| p.pci_address)
            .map(|a| format!("{a:?}"))
            .unwrap_or_default();
        gpus.push(Gpu {
            index: gpus.len(),
            key: format!("{} {} {address}", identity.name, identity.fingerprint),
            name: identity.name,
            device,
        });
    }
    Ok(gpus)
}

/// Run `f`, turning a Burn panic into an error naming `what`.
fn guard<T>(what: &str, f: impl FnOnce() -> Result<T>) -> Result<T> {
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(result) => result,
        Err(panic) => {
            let message = panic
                .downcast_ref::<String>()
                .cloned()
                .or_else(|| panic.downcast_ref::<&str>().map(|s| s.to_string()))
                .unwrap_or_else(|| "the runtime panicked".into());
            Err(Error::Device(format!("vulkan: {what}: {message}")))
        }
    }
}

/// The precision a GPU runs at: half where its shaders compute in half, else single.
pub(crate) fn dtype_for(gpu: &Gpu, requested: &str) -> Result<DType> {
    match requested {
        "" if gpu.device.supports_dtype(DType::F16) => Ok(DType::F16),
        "" | "f32" => Ok(DType::F32),
        "f16" if gpu.device.supports_dtype(DType::F16) => Ok(DType::F16),
        "f16" => Err(Error::Device(format!("{} does not compute in f16", gpu.name))),
        other => Err(Error::Device(format!("unknown dtype {other:?}; choose f16 or f32"))),
    }
}

/// The checkpoint's tensors, uploaded at the runtime dtype as they are taken.
struct Weights<'a> {
    tensors: HashMap<String, CandleTensor>,
    device: &'a Device,
    dtype: DType,
    label: String,
}

impl Weights<'_> {
    fn take<const D: usize>(&mut self, name: &str) -> Result<Tensor<D>> {
        let t = self
            .tensors
            .remove(name)
            .ok_or_else(|| Error::Checkpoint(format!("{}: no tensor {name}", self.label)))?;
        upload(&t, self.device, self.dtype)
            .map_err(|e| Error::Checkpoint(format!("{}: {name}: {e}", self.label)))
    }

    fn has(&self, name: &str) -> bool {
        self.tensors.contains_key(name)
    }
}

fn upload<const D: usize>(t: &CandleTensor, device: &Device, dtype: DType) -> Result<Tensor<D>> {
    let shape = t.dims().to_vec();
    if shape.len() != D {
        return Err(Error::Checkpoint(format!("rank {} where {D} was expected", shape.len())));
    }
    let values = t.to_dtype(candle_core::DType::F32)?.flatten_all()?.to_vec1::<f32>()?;
    Ok(Tensor::<D>::from_data(TensorData::new(values, shape), (device, DType::F32)).cast(dtype))
}

/// A linear layer stored with its weight transposed to `[in, out]`.
#[derive(Clone)]
struct Linear {
    weight_t: Tensor<2>,
    bias: Option<Tensor<1>>,
}

impl Linear {
    fn load(weights: &mut Weights, prefix: &str, bias: bool) -> Result<Self> {
        Self::from_parts(weights, &format!("{prefix}.weight"), bias.then(|| format!("{prefix}.bias")))
    }

    fn from_parts(weights: &mut Weights, weight: &str, bias: Option<String>) -> Result<Self> {
        let weight_t = weights.take::<2>(weight)?.transpose();
        let bias = match bias {
            Some(name) => Some(weights.take::<1>(&name)?),
            None => None,
        };
        Ok(Self { weight_t, bias })
    }

    fn forward(&self, x: Tensor<3>) -> Tensor<3> {
        let y = x.matmul(self.weight_t.clone().unsqueeze::<3>());
        match &self.bias {
            Some(b) => y + b.clone().unsqueeze::<3>(),
            None => y,
        }
    }
}

#[derive(Clone)]
struct LayerNorm {
    weight: Tensor<1>,
    bias: Option<Tensor<1>>,
    eps: f64,
}

impl LayerNorm {
    fn load(weights: &mut Weights, prefix: &str, bias: bool, eps: f64) -> Result<Self> {
        Ok(Self {
            weight: weights.take(&format!("{prefix}.weight"))?,
            bias: if bias { Some(weights.take(&format!("{prefix}.bias"))?) } else { None },
            eps,
        })
    }

    fn forward(&self, x: Tensor<3>) -> Tensor<3> {
        module::layer_norm(x, self.weight.clone(), self.bias.clone(), self.eps)
    }
}

/// `[B, H, L, D]` attention heads out of a `[B, L, H * D]` projection slice.
fn heads(x: Tensor<3>, heads: usize) -> Tensor<4> {
    let [b, l, d] = x.dims();
    x.reshape([b, l, heads, d / heads]).swap_dims(1, 2)
}

/// `[B, H, L, D]` back to `[B, L, H * D]`.
fn merge(x: Tensor<4>) -> Tensor<3> {
    let [b, h, l, d] = x.dims();
    x.swap_dims(1, 2).reshape([b, l, h * d])
}

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

struct Layer {
    attn_norm: Option<LayerNorm>,
    qkv: Linear,
    proj: Linear,
    mlp_norm: LayerNorm,
    wi: Linear,
    wo: Linear,
    local: bool,
}

/// The ModernBERT backbone.
struct Backbone {
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
    fn load(weights: &mut Weights, cfg: &Config) -> Result<Self> {
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
    fn forward(&self, ids: Tensor<2, Int>, mask: &Tensor<2>, dtype: DType) -> Tensor<3> {
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

/// `[B, L]` 0/1 → `[B, 1, 1, L]` with `fill` on the zeros.
fn padding_bias(mask: &Tensor<2>, fill: f64, dtype: DType) -> Tensor<4> {
    let [b, l] = mask.dims();
    ((mask.clone().neg() + 1.0) * fill).reshape([b, 1, 1, l]).cast(dtype)
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
    use super::*;
    use crate::device::Selected;
    use crate::engine::{Engine, EngineOptions, HEAD_CODE_RANK, Prepared};
    use crate::testutil::{assert_agrees_with_cpu, scratch_dir, write_tiny_checkpoint, write_tiny_head};

    /// The first GPU, else a software rasterizer, so parity runs wherever Mesa's lavapipe is
    /// installed.
    fn test_gpu() -> Gpu {
        gpus().first().cloned().unwrap_or_else(|| {
            enumerate(true)
                .unwrap()
                .into_iter()
                .next()
                .expect("no Vulkan device: install a GPU driver or Mesa's lavapipe")
        })
    }

    fn engine_on_test_gpu(dir: &Path, head: &Path, dtype: &str) -> Engine {
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            dtype: dtype.into(),
            heads: vec![(HEAD_CODE_RANK.to_string(), head.to_path_buf())],
            ..Default::default()
        };
        let prepared = Prepared::read(dir).unwrap();
        Engine::on_selected(&prepared, Selected::Vulkan(test_gpu()), &opts).unwrap()
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
    fn half_precision_stays_close_to_the_cpu() {
        let gpu = test_gpu();
        if !gpu.device.supports_dtype(DType::F16) {
            assert!(dtype_for(&gpu, "f16").is_err());
            assert_eq!(dtype_for(&gpu, "").unwrap(), DType::F32);
            return;
        }
        let dir = scratch_dir("vulkan-f16");
        write_tiny_checkpoint(&dir);
        let head = write_tiny_head(&dir, "rank", "tiny");
        let engine = engine_on_test_gpu(&dir, &head, "");
        assert_eq!(engine.identity().dtype, "f16");
        assert_agrees_with_cpu(&dir, &head, &engine, 3e-2);
    }

    #[test]
    fn a_runtime_panic_becomes_a_device_error() {
        let err = guard("probing", || -> Result<()> { panic!("device lost") }).unwrap_err();
        assert!(
            matches!(err, Error::Device(ref m) if m.contains("probing") && m.contains("device lost")),
            "{err}"
        );
        assert_eq!(guard("probing", || Ok(7)).unwrap(), 7);
    }

    #[test]
    fn every_gpu_is_keyed_by_its_part_and_slot() {
        for (i, gpu) in gpus().iter().enumerate() {
            assert_eq!(gpu.index, i);
            assert!(gpu.key.starts_with(&gpu.name), "{}", gpu.key);
        }
    }
}
