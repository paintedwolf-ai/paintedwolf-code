use crate::{
    execution::{Begin, Progress},
    rendering::RenderRequest,
};
use serde::{Deserialize, Serialize};

pub const VERSION: u32 = 1;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Envelope {
    pub version: u32,
    pub id: u64,
    pub command: Command,
}

#[derive(Deserialize)]
#[serde(tag = "method", rename_all = "snake_case", deny_unknown_fields)]
pub enum Command {
    Hello {},
    Load {
        context_tokens: usize,
        prefill_chunk: usize,
    },
    Render {
        request: RenderRequest,
        max_prompt_tokens: usize,
    },
    Begin {
        request: Begin,
    },
    Advance {
        request_id: u64,
    },
    Cancel {
        request_id: u64,
    },
    ClearPrefix {},
    Unload {},
    Shutdown {},
}

#[derive(Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Output {
    Hello {
        protocol: u32,
        backend: &'static str,
        artifact_id: String,
        physical_device: PhysicalDevice,
    },
    Loaded {
        context_tokens: usize,
        prefill_chunk: usize,
        sampling: super::settings::SamplingDefaults,
    },
    Rendered {
        prompt_tokens: Vec<u32>,
    },
    Begun {
        request_id: u64,
        reused_prefix_tokens: usize,
    },
    Progress {
        progress: Progress,
        text: String,
    },
    Cleared,
    Unloaded,
    Shutdown,
}

#[derive(Clone, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum PhysicalDevice {
    AppleSilicon,
    Pci { address: String },
}

#[derive(Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum Response {
    Ok {
        id: u64,
        result: Output,
    },
    Error {
        id: u64,
        code: Code,
        message: String,
    },
}

#[derive(Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Code {
    InvalidRequest,
    Capacity,
    Artifact,
    Device,
}
