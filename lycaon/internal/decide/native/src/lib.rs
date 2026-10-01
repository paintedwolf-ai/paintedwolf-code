//! Bialy: the tuned decision engine served over stdio.
//!
//! Decision heads share one resident backbone. Requests use line-delimited JSON;
//! see [`protocol`] and the host client in `lycaon/internal/decide/bialy`.

#![forbid(unsafe_code)]

pub mod calibration;
pub mod checkpoint;
pub mod config;
pub mod device;
pub mod engine;
pub mod error;
pub mod heads;
pub mod protocol;
pub mod pyjson;
pub mod question;

#[cfg(feature = "mlx")]
mod mlx;
mod model;
mod modernbert;
mod sequence;
#[cfg(test)]
mod testutil;
mod tokenizer;

pub use device::DeviceChoice;
pub use engine::{Engine, EngineOptions, Identity};
pub use error::{Error, Result};
