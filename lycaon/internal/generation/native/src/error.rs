//! Error type for the generation service.

/// Errors from loading, protocol handling or generation.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("checkpoint: {0}")]
    Checkpoint(String),
    #[error("tokenizer: {0}")]
    Tokenizer(String),
    #[error("device: {0}")]
    Device(String),
    #[error("protocol: {0}")]
    Protocol(String),
    #[error("capacity: {0}")]
    Capacity(String),
    #[error("mlx: {0}")]
    Mlx(String),
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
}

pub type Result<T> = std::result::Result<T, Error>;
