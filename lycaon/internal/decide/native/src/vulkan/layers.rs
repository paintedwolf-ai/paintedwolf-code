//! The building blocks the backbone and the heads share on the Vulkan runtime.

use std::collections::HashMap;

use burn::tensor::{DType, Device, Tensor, TensorData, module};
use candle_core::Tensor as CandleTensor;

use crate::error::{Error, Result};

/// The checkpoint's tensors, uploaded at the runtime dtype as they are taken.
pub(super) struct Weights<'a> {
    pub tensors: HashMap<String, CandleTensor>,
    pub device: &'a Device,
    pub dtype: DType,
    pub label: String,
}

impl Weights<'_> {
    pub fn take<const D: usize>(&mut self, name: &str) -> Result<Tensor<D>> {
        let t = self
            .tensors
            .remove(name)
            .ok_or_else(|| Error::Checkpoint(format!("{}: no tensor {name}", self.label)))?;
        upload(&t, self.device, self.dtype)
            .map_err(|e| Error::Checkpoint(format!("{}: {name}: {e}", self.label)))
    }

    pub fn has(&self, name: &str) -> bool {
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
pub(super) struct Linear {
    weight_t: Tensor<2>,
    bias: Option<Tensor<1>>,
}

impl Linear {
    pub fn load(weights: &mut Weights, prefix: &str, bias: bool) -> Result<Self> {
        Self::from_parts(weights, &format!("{prefix}.weight"), bias.then(|| format!("{prefix}.bias")))
    }

    pub fn from_parts(weights: &mut Weights, weight: &str, bias: Option<String>) -> Result<Self> {
        let weight_t = weights.take::<2>(weight)?.transpose();
        let bias = match bias {
            Some(name) => Some(weights.take::<1>(&name)?),
            None => None,
        };
        Ok(Self { weight_t, bias })
    }

    pub fn forward(&self, x: Tensor<3>) -> Tensor<3> {
        let y = x.matmul(self.weight_t.clone().unsqueeze::<3>());
        match &self.bias {
            Some(b) => y + b.clone().unsqueeze::<3>(),
            None => y,
        }
    }
}

#[derive(Clone)]
pub(super) struct LayerNorm {
    weight: Tensor<1>,
    bias: Option<Tensor<1>>,
    eps: f64,
}

impl LayerNorm {
    pub fn load(weights: &mut Weights, prefix: &str, bias: bool, eps: f64) -> Result<Self> {
        Ok(Self {
            weight: weights.take(&format!("{prefix}.weight"))?,
            bias: if bias { Some(weights.take(&format!("{prefix}.bias"))?) } else { None },
            eps,
        })
    }

    pub fn forward(&self, x: Tensor<3>) -> Tensor<3> {
        module::layer_norm(x, self.weight.clone(), self.bias.clone(), self.eps)
    }
}

/// `[B, H, L, D]` attention heads out of a `[B, L, H * D]` projection slice.
pub(super) fn heads(x: Tensor<3>, heads: usize) -> Tensor<4> {
    let [b, l, d] = x.dims();
    x.reshape([b, l, heads, d / heads]).swap_dims(1, 2)
}

/// `[B, H, L, D]` back to `[B, L, H * D]`.
pub(super) fn merge(x: Tensor<4>) -> Tensor<3> {
    let [b, h, l, d] = x.dims();
    x.swap_dims(1, 2).reshape([b, l, h * d])
}

/// `[B, L]` 0/1 → `[B, 1, 1, L]` with `fill` on the zeros.
pub(super) fn padding_bias(mask: &Tensor<2>, fill: f64, dtype: DType) -> Tensor<4> {
    let [b, l] = mask.dims();
    ((mask.clone().neg() + 1.0) * fill).reshape([b, 1, 1, l]).cast(dtype)
}
