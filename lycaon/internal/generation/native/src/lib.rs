pub mod error;
pub mod execution;
pub mod protocol;
pub mod rendering;
pub mod rendering_stream;
pub mod runner;
pub mod sampler;

pub use error::{Error, Result};

#[cfg(all(feature = "mlx", target_os = "macos", target_arch = "aarch64"))]
pub mod mlx_engine;

#[cfg(all(feature = "vulkan", target_os = "linux"))]
pub mod vulkan;
