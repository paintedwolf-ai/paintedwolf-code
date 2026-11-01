use super::{Backend, Error, Executor, Result};
use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Prepare {
    pub request_id: u64,
    pub prompt_tokens: Vec<u32>,
}

#[derive(Debug, Serialize)]
pub struct PreparationProgress {
    pub request_id: u64,
    pub processed_prompt_tokens: usize,
    pub reused_prefix_tokens: usize,
    pub complete: bool,
    pub canceled: bool,
}

pub(super) struct Preparation {
    request: Prepare,
    processed: usize,
    reused: usize,
}

impl<B: Backend> Executor<B> {
    pub fn prepare(&mut self, request: Prepare) -> Result<usize> {
        if self.active.is_some() || self.preparing.is_some() {
            return Err(Error::Protocol("a request is active".into()));
        }
        if request.request_id <= self.last_request
            || request.prompt_tokens.is_empty()
            || request.prompt_tokens.len() >= self.context
            || request.prompt_tokens.iter().any(|id| *id >= 262144)
        {
            return Err(Error::Protocol("invalid preparation request".into()));
        }
        let reused = self.resume_prefix(&request.prompt_tokens, true)?;
        self.last_request = request.request_id;
        self.preparing = Some(Preparation {
            request,
            processed: reused,
            reused,
        });
        Ok(reused)
    }

    pub fn advance_preparation(&mut self, request_id: u64) -> Result<PreparationProgress> {
        if self
            .preparing
            .as_ref()
            .is_none_or(|p| p.request.request_id != request_id)
        {
            return Err(Error::Protocol("preparation is not active".into()));
        }
        let mut preparation = self.preparing.take().unwrap();
        match self.prefill(&mut preparation) {
            Ok(progress) => {
                if !progress.complete {
                    self.preparing = Some(preparation);
                }
                Ok(progress)
            }
            Err(error) => {
                self.evaluated.clear();
                self.prefix.clear();
                self.backend.reset()?;
                self.backend.clear_prefix()?;
                Err(error)
            }
        }
    }

    fn prefill(&mut self, preparation: &mut Preparation) -> Result<PreparationProgress> {
        let tokens = &preparation.request.prompt_tokens;
        let end = (preparation.processed + self.chunk).min(tokens.len());
        if preparation.processed < end {
            if self
                .backend
                .forward(&tokens[preparation.processed..end], None)?
                .is_some()
            {
                return Err(Error::Device("preparation sampled a token".into()));
            }
            self.evaluated
                .extend_from_slice(&tokens[preparation.processed..end]);
            preparation.processed = end;
        }
        let complete = end == tokens.len();
        // Keep the shorter checkpoint when history can still change before submission.
        let retains_ancestor = !self.prefix.is_empty() && tokens.starts_with(&self.prefix);
        if complete && !retains_ancestor {
            self.backend.save_prefix()?;
            self.prefix.clone_from(tokens);
        }
        Ok(PreparationProgress {
            request_id: preparation.request.request_id,
            processed_prompt_tokens: end,
            reused_prefix_tokens: preparation.reused,
            complete,
            canceled: false,
        })
    }

    pub fn cancel_preparation(&mut self, request_id: u64) -> Result<PreparationProgress> {
        if self
            .preparing
            .as_ref()
            .is_none_or(|p| p.request.request_id != request_id)
        {
            return Err(Error::Protocol("preparation is not active".into()));
        }
        let preparation = self.preparing.take().unwrap();
        self.evaluated.clear();
        self.backend.reset()?;
        Ok(PreparationProgress {
            request_id,
            processed_prompt_tokens: preparation.processed,
            reused_prefix_tokens: preparation.reused,
            complete: false,
            canceled: true,
        })
    }
}
