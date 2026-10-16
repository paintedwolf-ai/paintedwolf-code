//! Choosing where to run.
//!
//! A device is named `auto`, `cpu`, `mlx`, `vulkan` or `vulkan:N`; the CLI's `--device` takes
//! that spelling. `mlx` needs the `mlx` feature (Apple silicon) and `vulkan` the `vulkan`
//! feature, where `N` indexes the GPUs Vulkan reports. `auto` measures every candidate this
//! build can reach and keeps the fastest; see [`crate::select`].

use std::str::FromStr;

use crate::error::{Error, Result};

/// Where the model should run.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum DeviceChoice {
    /// The fastest candidate that meets the speed requirement.
    #[default]
    Auto,
    Cpu,
    /// Apple's MLX runtime. Needs the `mlx` feature.
    Mlx,
    /// A Vulkan GPU, by its index among the GPUs Vulkan reports. Needs the `vulkan` feature.
    Vulkan(usize),
}

/// A resolved choice, ready to load a model onto.
pub(crate) enum Selected {
    Cpu,
    #[cfg(feature = "mlx")]
    Mlx,
    #[cfg(feature = "vulkan")]
    Vulkan(crate::vulkan::Gpu),
}

impl FromStr for DeviceChoice {
    type Err = Error;

    fn from_str(s: &str) -> Result<Self> {
        let spec = s.trim().to_lowercase();
        let (kind, index) = match spec.split_once(':') {
            Some((kind, n)) => {
                let n = n.parse().map_err(|_| {
                    Error::Device(format!("{s:?}: the device index must be a number"))
                })?;
                (kind, Some(n))
            }
            None => (spec.as_str(), None),
        };
        match (kind, index) {
            ("auto", None) => Ok(DeviceChoice::Auto),
            ("cpu", None) => Ok(DeviceChoice::Cpu),
            ("mlx", None) => Ok(DeviceChoice::Mlx),
            ("vulkan", n) => Ok(DeviceChoice::Vulkan(n.unwrap_or(0))),
            _ => Err(Error::Device(format!(
                "unknown device {s:?}; choose auto, cpu, mlx, vulkan or vulkan:N"
            ))),
        }
    }
}

impl std::fmt::Display for DeviceChoice {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            DeviceChoice::Auto => f.write_str("auto"),
            DeviceChoice::Cpu => f.write_str("cpu"),
            DeviceChoice::Mlx => f.write_str("mlx"),
            DeviceChoice::Vulkan(n) => write!(f, "vulkan:{n}"),
        }
    }
}

impl DeviceChoice {
    /// Open an explicit device. An accelerator that is not compiled in or does not open is an
    /// error, never a quiet CPU run.
    pub(crate) fn select(self) -> Result<Selected> {
        match self {
            DeviceChoice::Auto => {
                Err(Error::Device("auto is resolved by measuring candidates, not opened".into()))
            }
            DeviceChoice::Cpu => Ok(Selected::Cpu),
            DeviceChoice::Mlx => open_mlx(),
            DeviceChoice::Vulkan(n) => open_vulkan(n),
        }
    }

    /// Every device this build can reach, in the order `auto` measures them.
    pub(crate) fn candidates() -> Vec<DeviceChoice> {
        let mut out = Vec::new();
        if cfg!(feature = "mlx") {
            out.push(DeviceChoice::Mlx);
        }
        #[cfg(feature = "vulkan")]
        out.extend((0..crate::vulkan::gpus().len()).map(DeviceChoice::Vulkan));
        out.push(DeviceChoice::Cpu);
        out
    }

    /// What identifies this device's hardware across launches, for the cached verdict.
    pub(crate) fn hardware_key(self) -> String {
        match self {
            #[cfg(feature = "vulkan")]
            DeviceChoice::Vulkan(n) => match crate::vulkan::gpus().get(n) {
                Some(gpu) => format!("{self} {}", gpu.key),
                None => self.to_string(),
            },
            _ => self.to_string(),
        }
    }
}

#[cfg(feature = "mlx")]
fn open_mlx() -> Result<Selected> {
    Ok(Selected::Mlx)
}

#[cfg(not(feature = "mlx"))]
fn open_mlx() -> Result<Selected> {
    Err(Error::Device("mlx was asked for, but this build has the `mlx` feature off".into()))
}

#[cfg(feature = "vulkan")]
fn open_vulkan(n: usize) -> Result<Selected> {
    let gpus = crate::vulkan::gpus();
    match gpus.get(n) {
        Some(gpu) => Ok(Selected::Vulkan(gpu.clone())),
        None => Err(Error::Device(format!(
            "vulkan:{n} was asked for, but Vulkan reports {} GPU(s)",
            gpus.len()
        ))),
    }
}

#[cfg(not(feature = "vulkan"))]
fn open_vulkan(n: usize) -> Result<Selected> {
    Err(Error::Device(format!(
        "vulkan:{n} was asked for, but this build has the `vulkan` feature off"
    )))
}

/// A short name for a resolved device, for logs and the handshake.
pub(crate) fn describe(selected: &Selected) -> String {
    match selected {
        Selected::Cpu => "cpu".to_string(),
        #[cfg(feature = "mlx")]
        Selected::Mlx => "mlx".to_string(),
        #[cfg(feature = "vulkan")]
        Selected::Vulkan(gpu) => format!("vulkan:{}", gpu.index),
    }
}

/// The hardware's own name, when it has one worth reporting.
pub(crate) fn adapter(selected: &Selected) -> String {
    match selected {
        #[cfg(feature = "vulkan")]
        Selected::Vulkan(gpu) => gpu.name.clone(),
        _ => String::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn device_names_parse() {
        for (s, want) in [
            ("auto", DeviceChoice::Auto),
            ("CPU", DeviceChoice::Cpu),
            ("mlx", DeviceChoice::Mlx),
            ("vulkan", DeviceChoice::Vulkan(0)),
            ("vulkan:1", DeviceChoice::Vulkan(1)),
        ] {
            assert_eq!(s.parse::<DeviceChoice>().unwrap(), want, "{s}");
        }
        for bad in ["tpu", "cuda", "metal", "vulkan:x", "cpu:1", "mlx:1", ""] {
            assert!(bad.parse::<DeviceChoice>().is_err(), "{bad}");
        }
        assert_eq!(DeviceChoice::Vulkan(1).to_string(), "vulkan:1");
    }

    #[test]
    fn the_cpu_always_resolves_and_is_always_a_candidate() {
        assert!(matches!(DeviceChoice::Cpu.select().unwrap(), Selected::Cpu));
        assert_eq!(DeviceChoice::candidates().last(), Some(&DeviceChoice::Cpu));
    }

    #[cfg(not(feature = "vulkan"))]
    #[test]
    fn an_explicit_gpu_without_its_feature_is_an_error_not_a_cpu_run() {
        let err = DeviceChoice::Vulkan(0).select().err().unwrap();
        assert!(matches!(err, Error::Device(ref m) if m.contains("`vulkan` feature")), "{err}");
    }
}
