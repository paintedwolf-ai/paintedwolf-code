//! The runtimes a checkpoint can be resident on: candle on the CPU, MLX on Apple silicon, and
//! Burn on any Vulkan GPU. Each loads the same checkpoint and heads and answers a collated
//! batch with the same logits up to rounding.

use std::path::Path;

use candle_core::{DType, Device};

use crate::checkpoint::{Checkpoint, verify_tensors};
use crate::config::AgentConfig;
use crate::device::Selected;
use crate::error::{Error, Result};
use crate::heads;
use crate::model::{DecisionModel, Forward, Head};
use crate::modernbert;
use crate::sequence::Batch;

pub(crate) enum Runtime {
    Candle(DecisionModel),
    #[cfg(feature = "mlx")]
    Mlx(crate::mlx::Model),
    #[cfg(feature = "vulkan")]
    Vulkan(crate::vulkan::Model),
}

/// A head loaded for the runtime the engine runs.
#[derive(Clone)]
pub(crate) enum RuntimeHead {
    Candle(Head),
    #[cfg(feature = "mlx")]
    Mlx(crate::mlx::Head),
    #[cfg(feature = "vulkan")]
    Vulkan(crate::vulkan::Head),
}

/// What a runtime is loaded with besides the device.
pub(crate) struct LoadSpec<'a> {
    pub checkpoint: &'a Checkpoint,
    pub config: &'a AgentConfig,
    pub encoder: &'a modernbert::Config,
    /// `f16`, `f32`, or empty for the device's own precision.
    pub dtype: &'a str,
    #[cfg(feature = "mlx")]
    pub metallib: Option<&'a Path>,
}

impl Runtime {
    /// Load the checkpoint onto `selected`, returning the runtime and the checkpoint's own head.
    pub(crate) fn load(selected: &Selected, spec: &LoadSpec) -> Result<(Runtime, RuntimeHead)> {
        let cp = spec.checkpoint;
        match selected {
            Selected::Cpu => {
                let dtype = match spec.dtype {
                    "" | "f32" => DType::F32,
                    "f16" => DType::F16,
                    other => return Err(unknown_dtype(other)),
                };
                let weights = candle_weights(spec)?;
                let model = DecisionModel::load(
                    weights,
                    spec.encoder,
                    spec.config.head_layers,
                    dtype,
                    &Device::Cpu,
                )?;
                let base = RuntimeHead::Candle(model.base_head().clone());
                Ok((Runtime::Candle(model), base))
            }
            #[cfg(feature = "mlx")]
            Selected::Mlx => {
                let dtype = match spec.dtype {
                    "" | "f16" => mlx_rs::Dtype::Float16,
                    "f32" => mlx_rs::Dtype::Float32,
                    other => return Err(unknown_dtype(other)),
                };
                if let Some(path) = spec.metallib {
                    crate::mlx::set_metallib(path)?;
                }
                let weights = mlx_rs::Array::load_safetensors(&cp.weights)
                    .map_err(|e| Error::Checkpoint(format!("{}: {e}", cp.weights.display())))?;
                verify_tensors(
                    weights.keys(),
                    &cp.label,
                    spec.config.head_layers,
                    spec.encoder.num_hidden_layers,
                )?;
                let model = crate::mlx::Model::load(
                    weights,
                    spec.encoder,
                    spec.config.head_layers,
                    dtype,
                    &cp.label,
                )?;
                let base = RuntimeHead::Mlx(model.base_head().clone());
                Ok((Runtime::Mlx(model), base))
            }
            #[cfg(feature = "vulkan")]
            Selected::Vulkan(gpu) => {
                let dtype = crate::vulkan::dtype_for(gpu, spec.dtype)?;
                let weights = candle_weights(spec)?;
                let model = crate::vulkan::Model::load(
                    weights,
                    spec.encoder,
                    spec.config.head_layers,
                    dtype,
                    gpu.clone(),
                    &cp.label,
                )?;
                let base = RuntimeHead::Vulkan(model.base_head().clone());
                Ok((Runtime::Vulkan(model), base))
            }
        }
    }

    pub(crate) fn dtype_name(&self) -> &'static str {
        let half = match self {
            Runtime::Candle(m) => m.dtype() == DType::F16,
            #[cfg(feature = "mlx")]
            Runtime::Mlx(m) => m.dtype() == mlx_rs::Dtype::Float16,
            #[cfg(feature = "vulkan")]
            Runtime::Vulkan(m) => m.dtype() == burn::tensor::DType::F16,
        };
        if half { "f16" } else { "f32" }
    }

    /// Load a head file for this runtime.
    pub(crate) fn load_head(&self, path: &Path, label: &str) -> Result<RuntimeHead> {
        match self {
            Runtime::Candle(m) => {
                let file = heads::read(path)?;
                Ok(RuntimeHead::Candle(Head::from_tensors(
                    file.tensors,
                    m.shape(),
                    label,
                    m.dtype(),
                    &Device::Cpu,
                )?))
            }
            #[cfg(feature = "mlx")]
            Runtime::Mlx(m) => {
                Ok(RuntimeHead::Mlx(crate::mlx::Head::from_file(path, m.shape(), m.dtype())?))
            }
            #[cfg(feature = "vulkan")]
            Runtime::Vulkan(m) => {
                let file = heads::read(path)?;
                Ok(RuntimeHead::Vulkan(crate::vulkan::Head::from_tensors(
                    file.tensors,
                    m.shape(),
                    label,
                    m,
                )?))
            }
        }
    }

    pub(crate) fn forward(&self, head: &RuntimeHead, batch: &Batch) -> Result<Forward> {
        match (self, head) {
            (Runtime::Candle(m), RuntimeHead::Candle(h)) => m.forward(h, batch),
            #[cfg(feature = "mlx")]
            (Runtime::Mlx(m), RuntimeHead::Mlx(h)) => m.forward(h, batch),
            #[cfg(feature = "vulkan")]
            (Runtime::Vulkan(m), RuntimeHead::Vulkan(h)) => m.forward(h, batch),
            #[allow(unreachable_patterns)]
            _ => Err(Error::Head("a head loaded for one runtime cannot run on another".into())),
        }
    }
}

/// `model.safetensors` read onto the CPU and checked against its config before any device
/// allocation.
fn candle_weights(spec: &LoadSpec) -> Result<std::collections::HashMap<String, candle_core::Tensor>> {
    let cp = spec.checkpoint;
    let weights = candle_core::safetensors::load(&cp.weights, &Device::Cpu)?;
    verify_tensors(
        weights.keys(),
        &cp.label,
        spec.config.head_layers,
        spec.encoder.num_hidden_layers,
    )?;
    Ok(weights)
}

fn unknown_dtype(other: &str) -> Error {
    Error::Device(format!("unknown dtype {other:?}; choose f16 or f32"))
}
