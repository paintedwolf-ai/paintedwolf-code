//! Choosing where to run.
//!
//! A device is named `auto`, `cpu`, `cuda`, `cuda:N`, `metal`, `metal:N` or `mlx`; the CLI's
//! `--device` takes that spelling. `mlx` is Apple's runtime for the same model (the `mlx`
//! feature, Apple silicon only); `auto` prefers it where it is compiled in.

use std::str::FromStr;

use candle_core::Device;

use crate::error::{Error, Result};

/// Where the model should run, before it is resolved into a candle [`Device`].
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum DeviceChoice {
    /// The best accelerator this build can reach, else the CPU.
    #[default]
    Auto,
    Cpu,
    /// An NVIDIA GPU, by index. Needs the `cuda` feature.
    Cuda(usize),
    /// An Apple GPU, by index, through candle. Needs the `metal` feature.
    Metal(usize),
    /// Apple's MLX runtime. Needs the `mlx` feature.
    Mlx,
}

/// A resolved choice: a candle device, or the MLX runtime.
#[derive(Clone, Debug)]
pub(crate) enum Selected {
    Candle(Device),
    #[cfg(feature = "mlx")]
    Mlx,
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
            ("cuda" | "gpu", n) => Ok(DeviceChoice::Cuda(n.unwrap_or(0))),
            ("metal" | "mps", n) => Ok(DeviceChoice::Metal(n.unwrap_or(0))),
            ("mlx", None) => Ok(DeviceChoice::Mlx),
            _ => Err(Error::Device(format!(
                "unknown device {s:?}; choose auto, cpu, cuda, cuda:N, metal, metal:N or mlx"
            ))),
        }
    }
}

impl std::fmt::Display for DeviceChoice {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            DeviceChoice::Auto => f.write_str("auto"),
            DeviceChoice::Cpu => f.write_str("cpu"),
            DeviceChoice::Cuda(n) => write!(f, "cuda:{n}"),
            DeviceChoice::Metal(n) => write!(f, "metal:{n}"),
            DeviceChoice::Mlx => f.write_str("mlx"),
        }
    }
}

impl DeviceChoice {
    /// Open the device.
    ///
    /// `auto` takes MLX where it is compiled in, else the first accelerator that opens, else
    /// the CPU; an explicit accelerator that is not compiled in or does not open is an error.
    pub(crate) fn select(self) -> Result<Selected> {
        match self {
            DeviceChoice::Cpu => Ok(Selected::Candle(Device::Cpu)),
            DeviceChoice::Cuda(n) => open_cuda(n).map(Selected::Candle),
            DeviceChoice::Metal(n) => open_metal(n).map(Selected::Candle),
            DeviceChoice::Mlx => open_mlx(),
            DeviceChoice::Auto => Ok(auto()),
        }
    }
}

#[cfg(feature = "mlx")]
fn open_mlx() -> Result<Selected> {
    Ok(Selected::Mlx)
}

#[cfg(not(feature = "mlx"))]
fn open_mlx() -> Result<Selected> {
    Err(Error::Device("mlx was asked for, but this build has the `mlx` feature off; rebuild with `--features mlx`".into()))
}

#[cfg(feature = "cuda")]
fn open_cuda(n: usize) -> Result<Device> {
    Device::new_cuda(n).map_err(|e| Error::Device(format!("cannot open cuda:{n}: {e}")))
}

#[cfg(not(feature = "cuda"))]
fn open_cuda(n: usize) -> Result<Device> {
    Err(Error::Device(format!(
        "cuda:{n} was asked for, but this build has the `cuda` feature off; rebuild with \
         `--features cuda`"
    )))
}

#[cfg(feature = "metal")]
fn open_metal(n: usize) -> Result<Device> {
    Device::new_metal(n).map_err(|e| Error::Device(format!("cannot open metal:{n}: {e}")))
}

#[cfg(not(feature = "metal"))]
fn open_metal(n: usize) -> Result<Device> {
    Err(Error::Device(format!(
        "metal:{n} was asked for, but this build has the `metal` feature off; rebuild with \
         `--features metal`"
    )))
}

/// MLX where compiled in, then CUDA, then Metal, then the CPU, warning about any compiled-in
/// accelerator that failed.
fn auto() -> Selected {
    #[cfg(feature = "mlx")]
    return Selected::Mlx;
    #[cfg(not(feature = "mlx"))]
    Selected::Candle(auto_candle())
}

#[cfg(not(feature = "mlx"))]
fn auto_candle() -> Device {
    #[cfg(feature = "cuda")]
    match Device::new_cuda(0) {
        Ok(d) => return d,
        Err(e) => {
            eprintln!("bialy: built with `cuda` but cuda:0 did not open ({e}); trying next")
        }
    }
    #[cfg(feature = "metal")]
    match Device::new_metal(0) {
        Ok(d) => return d,
        Err(e) => {
            eprintln!("bialy: built with `metal` but metal:0 did not open ({e}); trying next")
        }
    }
    #[cfg(any(feature = "cuda", feature = "metal"))]
    eprintln!("bialy: no accelerator available; running on the CPU");
    Device::Cpu
}

/// A short name for a device, for logs and the CLI.
pub(crate) fn describe(selected: &Selected) -> String {
    match selected {
        Selected::Candle(device) => match device.location() {
            candle_core::DeviceLocation::Cpu => "cpu".to_string(),
            candle_core::DeviceLocation::Cuda { gpu_id } => format!("cuda:{gpu_id}"),
            candle_core::DeviceLocation::Metal { .. } => "metal".to_string(),
        },
        #[cfg(feature = "mlx")]
        Selected::Mlx => "mlx".to_string(),
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
            ("cuda", DeviceChoice::Cuda(0)),
            ("cuda:1", DeviceChoice::Cuda(1)),
            ("gpu", DeviceChoice::Cuda(0)),
            ("metal", DeviceChoice::Metal(0)),
            ("mps:2", DeviceChoice::Metal(2)),
            ("mlx", DeviceChoice::Mlx),
        ] {
            assert_eq!(s.parse::<DeviceChoice>().unwrap(), want, "{s}");
        }
        for bad in ["tpu", "cuda:x", "cpu:1", "mlx:1", ""] {
            assert!(bad.parse::<DeviceChoice>().is_err(), "{bad}");
        }
        assert_eq!(DeviceChoice::Cuda(1).to_string(), "cuda:1");
    }

    #[test]
    fn the_cpu_always_resolves() {
        assert!(matches!(DeviceChoice::Cpu.select().unwrap(), Selected::Candle(d) if d.is_cpu()));
    }

    #[cfg(not(feature = "cuda"))]
    #[test]
    fn an_explicit_gpu_without_its_feature_is_an_error_not_a_cpu_run() {
        let err = DeviceChoice::Cuda(0).select().unwrap_err();
        assert!(matches!(err, Error::Device(ref m) if m.contains("--features cuda")), "{err}");
    }
}
