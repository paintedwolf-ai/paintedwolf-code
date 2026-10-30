//! Length-prefixed UTF-8 JSON protocol frames over stdio pipes.

use serde::{Deserialize, Serialize};

use crate::error::{Error, Result};

/// Maximum inbound frame size (8 MiB, sufficient for a 64K-token prompt plus metadata).
pub const MAX_FRAME_BYTES: u32 = 8 << 20;

pub fn write_frame<W: std::io::Write>(w: &mut W, payload: &[u8]) -> Result<()> {
    let len = u32::try_from(payload.len()).map_err(|_| Error::Protocol("frame too large".into()))?;
    w.write_all(&len.to_le_bytes())?;
    w.write_all(payload)?;
    w.flush()?;
    Ok(())
}

pub fn read_frame<R: std::io::Read>(r: &mut R) -> Result<Option<Vec<u8>>> {
    let mut len = [0u8; 4];
    if r.read_exact(&mut len).is_err() {
        return Ok(None);
    }
    let len = u32::from_le_bytes(len);
    if len > MAX_FRAME_BYTES {
        return Err(Error::Protocol(format!("frame of {len} bytes exceeds the limit")));
    }
    let mut buf = vec![0u8; len as usize];
    r.read_exact(&mut buf)?;
    Ok(Some(buf))
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(tag = "method", rename_all = "snake_case")]
pub enum Request {
    Hello,
    Load {
        manifest: String,
        weights: String,
        device: String,
        dtype: Option<String>,
    },
    Tokenize {
        text: String,
    },
    Generate(GenerateRequest),
    Cancel {
        request_id: String,
    },
    Shutdown,
}

#[derive(Debug, Deserialize)]
pub struct Inbound {
    #[serde(default)]
    pub id: u64,
    #[serde(flatten)]
    pub body: Request,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct GenerateRequest {
    pub request_id: String,
    pub prompt_tokens: Vec<u32>,
    pub max_new_tokens: u32,
    pub seed: u64,
    pub temperature: f32,
    pub top_p: f32,
    pub stop_tokens: Vec<u32>,
    pub cache_handle: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Event {
    Accepted {
        request_id: String,
        generation: u64,
    },
    Token {
        request_id: String,
        seq: u64,
        token: u32,
    },
    Usage {
        request_id: String,
        prompt_tokens: u64,
        generated_tokens: u64,
        reused_prefix: u64,
    },
    Done {
        request_id: String,
        stop_reason: StopReason,
    },
    Failed {
        request_id: String,
        error: String,
    },
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum StopReason {
    Eos,
    StopToken,
    MaxTokens,
    Canceled,
    Deadline,
}
