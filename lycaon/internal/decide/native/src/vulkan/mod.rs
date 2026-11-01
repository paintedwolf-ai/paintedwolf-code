//! Vulkan device selection, precision and persistent kernel caches.
//! Device faults become errors at enumeration, load and forward boundaries.

use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::Path;
use std::sync::OnceLock;

use burn::cubecl::config::cache::CacheConfig;
use burn::cubecl::config::{CubeClRuntimeConfig, RuntimeConfig};
use burn::tensor::{DType, Device, DeviceType};

use crate::error::{Error, Result};

mod layers;
mod model;
mod modernbert;

pub(crate) use model::{Head, Model};

/// One GPU Vulkan reports, with what identifies it across launches.
#[derive(Clone)]
pub(crate) struct Gpu {
    pub index: usize,
    /// The name the driver gives the part.
    pub name: String,
    /// Part, kernel target and bus address identify the qualification cache.
    pub key: String,
    pub pci_address: Option<String>,
    device: Device,
}

/// Select persistent caches before opening a device.
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

/// Enumerate hardware Vulkan devices in driver order.
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
        let pci_address =
            identity.physical.as_ref().and_then(|p| p.pci_address.as_ref()).map(|a| a.to_string());
        let address = identity
            .physical
            .as_ref()
            .and_then(|p| p.pci_address.as_ref())
            .map(|a| format!("{a:?}"))
            .unwrap_or_default();
        gpus.push(Gpu {
            index: gpus.len(),
            key: format!("{} {} {address}", identity.name, identity.fingerprint),
            name: identity.name,
            pci_address,
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

/// Parity fixtures may use a software Vulkan device.
#[cfg(test)]
fn test_gpu() -> Gpu {
    gpus().first().cloned().unwrap_or_else(|| {
        enumerate(true)
            .unwrap()
            .into_iter()
            .next()
            .expect("no Vulkan device: install a GPU driver or Mesa's lavapipe")
    })
}

#[cfg(test)]
mod tests {
    use super::*;

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

    #[test]
    fn half_precision_is_offered_only_where_the_gpu_computes_in_it() {
        let gpu = test_gpu();
        let half = gpu.device.supports_dtype(DType::F16);
        assert_eq!(dtype_for(&gpu, "").unwrap(), if half { DType::F16 } else { DType::F32 });
        assert_eq!(dtype_for(&gpu, "f16").is_ok(), half);
        assert_eq!(dtype_for(&gpu, "f32").unwrap(), DType::F32);
        assert!(dtype_for(&gpu, "bf16").is_err());
    }
}
