use serde::{Deserialize, Serialize};

use crate::error::{Error, Result};
use crate::protocol::StopReason;
use crate::sampler::{Rng, Sampling};

mod cache;
pub mod preparation;

pub trait Backend {
    fn plan(
        directory: &std::path::Path,
        context: usize,
        chunk: usize,
    ) -> Result<crate::resources::Plan>;
    // Return only after the submitted device work completes.
    fn forward(&mut self, tokens: &[u32], sampling: Option<&mut Sampling>) -> Result<Option<u32>>;
    fn reset(&mut self) -> Result<()>;
    fn save_prefix(&mut self) -> Result<()>;
    fn restore_prefix(&mut self) -> Result<bool>;
    fn clear_prefix(&mut self) -> Result<()>;
    fn release(self) -> Result<()>
    where
        Self: Sized,
    {
        drop(self);
        Ok(())
    }
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Begin {
    pub request_id: u64,
    pub output_contract: crate::output_contract::Contract,
    pub prompt_tokens: Vec<u32>,
    pub max_new_tokens: usize,
    pub temperature: f32,
    pub top_p: f32,
    pub top_k: usize,
    pub seed: u64,
    pub checkpoint_tokens: usize,
    #[serde(default)]
    pub stop_tokens: Vec<u32>,
    #[serde(default)]
    pub output_schema: Option<serde_json::Value>,
}

#[derive(Debug, Serialize)]
pub struct Progress {
    pub request_id: u64,
    pub processed_prompt_tokens: usize,
    pub generated_tokens: usize,
    pub reused_prefix_tokens: usize,
    pub token: Option<u32>,
    pub stop_reason: Option<StopReason>,
}

struct Generation {
    request: Begin,
    sampling: Sampling,
    processed: usize,
    generated: usize,
    reused: usize,
    last_token: Option<u32>,
}

pub struct Executor<B> {
    backend: B,
    context: usize,
    chunk: usize,
    eos: Vec<u32>,
    prefix: Vec<u32>,
    evaluated: Vec<u32>,
    active: Option<Generation>,
    preparing: Option<preparation::Preparation>,
    last_request: u64,
}

impl<B: Backend> Executor<B> {
    pub fn new(backend: B, context: usize, chunk: usize, eos: Vec<u32>) -> Result<Self> {
        if !(2..=65536).contains(&context)
            || !(1..=256).contains(&chunk)
            || !eos.iter().any(|id| matches!(id, 1 | 106))
            || eos.len() > 16
            || eos.iter().any(|id| !crate::output_contract::terminal(*id))
        {
            return Err(Error::Capacity("invalid execution limits".into()));
        }
        Ok(Self {
            backend,
            context,
            chunk,
            eos,
            prefix: Vec::new(),
            evaluated: Vec::new(),
            active: None,
            preparing: None,
            last_request: 0,
        })
    }

    pub fn begin(
        &mut self,
        request: Begin,
        constraint: Option<llguidance::Matcher>,
        mut envelope: crate::output_contract::Envelope,
    ) -> Result<usize> {
        if self.active.is_some() || self.preparing.is_some() {
            return Err(Error::Protocol("a request is active".into()));
        }
        if request.request_id <= self.last_request
            || request.prompt_tokens.is_empty()
            || !(1..=4096).contains(&request.max_new_tokens)
            || request
                .prompt_tokens
                .len()
                .saturating_add(request.max_new_tokens)
                > self.context
            || request.checkpoint_tokens >= request.prompt_tokens.len()
            || request
                .prompt_tokens
                .iter()
                .chain(&request.stop_tokens)
                .any(|id| *id >= 262144)
            || request.stop_tokens.iter().any(|id| {
                crate::output_contract::reserved(*id) && !crate::output_contract::terminal(*id)
            })
            || request.stop_tokens.len() > 16
            || request.top_k > 262144
            || !request.temperature.is_finite()
            || request.temperature < 0.0
            || !request.top_p.is_finite()
            || !(0.0 < request.top_p && request.top_p <= 1.0)
        {
            return Err(Error::Protocol("invalid generation request".into()));
        }
        envelope.set_terminals(self.eos.iter().chain(&request.stop_tokens).copied());
        let reused = self.resume_prefix(&request.prompt_tokens, false)?;
        self.last_request = request.request_id;
        let sampling = Sampling {
            temperature: request.temperature,
            top_p: request.top_p,
            top_k: request.top_k,
            rng: Rng::new(request.seed),
            constraint,
            envelope,
        };
        self.active = Some(Generation {
            request,
            sampling,
            processed: reused,
            generated: 0,
            reused,
            last_token: None,
        });
        Ok(reused)
    }

    pub fn advance(&mut self, request_id: u64) -> Result<Progress> {
        if self
            .active
            .as_ref()
            .is_none_or(|g| g.request.request_id != request_id)
        {
            return Err(Error::Protocol("request is not active".into()));
        }
        let mut generation = self.active.take().unwrap();
        let result = self.advance_generation(&mut generation);
        match result {
            Ok(progress) => {
                if progress.stop_reason.is_none() {
                    self.active = Some(generation);
                }
                Ok(progress)
            }
            Err(error) => {
                self.evaluated.clear();
                self.backend.reset()?;
                self.backend.clear_prefix()?;
                self.prefix.clear();
                Err(error)
            }
        }
    }

    fn advance_generation(&mut self, g: &mut Generation) -> Result<Progress> {
        let next = if g.processed < g.request.prompt_tokens.len() {
            let mut end = (g.processed + self.chunk).min(g.request.prompt_tokens.len());
            if g.processed < g.request.checkpoint_tokens {
                end = end.min(g.request.checkpoint_tokens);
            }
            let final_chunk = end == g.request.prompt_tokens.len();
            let next = self.backend.forward(
                &g.request.prompt_tokens[g.processed..end],
                final_chunk.then_some(&mut g.sampling),
            )?;
            self.evaluated
                .extend_from_slice(&g.request.prompt_tokens[g.processed..end]);
            g.processed = end;
            if end == g.request.checkpoint_tokens {
                self.backend.save_prefix()?;
                self.prefix = g.request.prompt_tokens[..end].to_vec();
            }
            if final_chunk != next.is_some() {
                return Err(Error::Device("invalid prefill result".into()));
            }
            next
        } else {
            let previous = g
                .last_token
                .ok_or_else(|| Error::Device("missing decode input".into()))?;
            let next = self
                .backend
                .forward(&[previous], Some(&mut g.sampling))?
                .ok_or_else(|| Error::Device("missing sampled token".into()))?;
            self.evaluated.push(previous);
            Some(next)
        };
        let mut token = next;
        let mut stop = None;
        if let Some(next) = next {
            if next >= 262144 {
                return Err(Error::Device("sampled token exceeds vocabulary".into()));
            }
            g.generated += 1;
            g.last_token = Some(next);
            if g.sampling.envelope.complete() && self.eos.contains(&next) {
                stop = Some(StopReason::Eos);
                token = None;
            } else if g.sampling.envelope.complete() && g.request.stop_tokens.contains(&next) {
                stop = Some(StopReason::StopToken);
                token = None;
            } else if g
                .sampling
                .constraint
                .as_mut()
                .map(crate::constraints::complete)
                .transpose()?
                .unwrap_or(false)
            {
                stop = Some(StopReason::StructuredOutput);
            } else if g.generated == g.request.max_new_tokens {
                stop = Some(StopReason::MaxTokens);
            }
        }
        Ok(Progress {
            request_id: g.request.request_id,
            processed_prompt_tokens: g.processed,
            generated_tokens: g.generated,
            reused_prefix_tokens: g.reused,
            token,
            stop_reason: stop,
        })
    }

    pub fn cancel(&mut self, request_id: u64) -> Result<Progress> {
        if self
            .active
            .as_ref()
            .is_none_or(|g| g.request.request_id != request_id)
        {
            return Err(Error::Protocol("request is not active".into()));
        }
        let generation = self.active.take().unwrap();
        self.evaluated.clear();
        self.backend.reset()?;
        Ok(Progress {
            request_id,
            processed_prompt_tokens: generation.processed,
            generated_tokens: generation.generated,
            reused_prefix_tokens: generation.reused,
            token: None,
            stop_reason: Some(StopReason::Canceled),
        })
    }

    pub fn clear_prefix(&mut self) -> Result<()> {
        if self.active.is_some() || self.preparing.is_some() {
            return Err(Error::Protocol("a request is active".into()));
        }
        self.evaluated.clear();
        self.backend.reset()?;
        self.backend.clear_prefix()?;
        self.prefix.clear();
        Ok(())
    }

    pub fn release(self) -> Result<()> {
        self.backend.release()
    }

    pub fn backend(&self) -> &B {
        &self.backend
    }
}

#[cfg(test)]
#[path = "execution_tests.rs"]
mod tests;
