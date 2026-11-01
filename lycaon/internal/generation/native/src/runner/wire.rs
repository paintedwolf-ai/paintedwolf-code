use crate::{
    execution::{
        Begin, Progress,
        preparation::{Prepare, PreparationProgress},
    },
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
    Inspect {
        context_tokens: usize,
        prefill_chunk: usize,
    },
    Load {
        context_tokens: usize,
        prefill_chunk: usize,
        device_budget_bytes: u64,
        host_budget_bytes: u64,
    },
    Measure {
        request: RenderRequest,
    },
    Render {
        request: RenderRequest,
        max_prompt_tokens: usize,
    },
    Prepare {
        request: Prepare,
    },
    AdvancePreparation {
        request_id: u64,
    },
    CancelPreparation {
        request_id: u64,
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
        quiescent_memory: bool,
        backend: &'static str,
        artifact_id: String,
        physical_device: PhysicalDevice,
    },
    Resources {
        plan: crate::resources::Plan,
    },
    Loaded {
        context_tokens: usize,
        prefill_chunk: usize,
        sampling: super::settings::SamplingDefaults,
    },
    Measured {
        #[serde(flatten)]
        measured: crate::rendering::Measurement,
    },
    Rendered {
        #[serde(flatten)]
        rendered: crate::rendering::Rendered,
    },
    Begun {
        request_id: u64,
        reused_prefix_tokens: usize,
    },
    PreparationProgress {
        progress: PreparationProgress,
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
