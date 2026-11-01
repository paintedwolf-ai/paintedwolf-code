//! Native local generation service for Gemma 4 E4B.

pub mod cache;
pub mod checkpoint;
pub mod decoder;
pub mod error;
pub mod protocol;
pub mod sampler;
pub mod service;
pub mod rendering;
pub mod execution;

pub use cache::Cache;
pub use decoder::Decoder;
pub use error::{Error, Result};
pub use service::Service;

#[cfg(feature = "mlx")]
pub mod mlx_engine;
