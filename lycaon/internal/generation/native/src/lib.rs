//! Native local generation for Gemma 4 E4B on the host's behalf.
//!
//! The crate owns the stdio protocol, the transactional KV cache, the reference
//! CPU decoder, sampling, and the service loop. Accelerated backends (MLX on
//! Apple silicon) plug in beside the CPU reference through the same decoder
//! interface.

pub mod cache;
pub mod checkpoint;
pub mod decoder;
pub mod error;
pub mod protocol;
pub mod sampler;
pub mod service;

pub use cache::Cache;
pub use decoder::Decoder;
pub use error::{Error, Result};
pub use service::Service;
