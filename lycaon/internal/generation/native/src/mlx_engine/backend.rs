use super::{Cache, Logits, Model};
use crate::{
    error::{Error, Result},
    execution::{Backend, Sampling},
};
use mlx_rs::{memory, ops::indexing::*, Dtype};
use std::path::Path;

fn device_error(error: impl std::fmt::Display) -> Error {
    Error::Device(error.to_string())
}

pub struct Mlx {
    model: Model,
    cache: Cache,
    prefix: Option<Cache>,
    budget: usize,
    idle_budget: usize,
}

impl Mlx {
    pub fn load(directory: &Path, context: usize, chunk: usize, budget: usize) -> Result<Self> {
        let plan = Self::plan(directory, context, chunk)?;
        let idle_budget = (plan.device_reservation_bytes - plan.workspace_bytes) as usize;
        memory::set_memory_limit(budget).map_err(device_error)?;
        memory::set_wired_limit(budget).map_err(device_error)?;
        memory::set_cache_limit(128 << 20).map_err(device_error)?;
        memory::reset_peak_memory().map_err(device_error)?;
        let engine = Self {
            model: Model::load(directory).map_err(device_error)?,
            cache: Cache::default(),
            prefix: None,
            budget,
            idle_budget,
        };
        engine.completed(None)?;
        engine.quiescent()?;
        Ok(engine)
    }

    fn quiescent(&self) -> Result<()> {
        // Both default streams may hold buffers after evaluated arrays are dropped.
        for stream in [mlx_rs::Stream::cpu(), mlx_rs::Stream::gpu()] {
            if unsafe { mlx_sys::mlx_synchronize(stream.as_ptr()) } != 0 {
                return Err(device_error("stream synchronization failed"));
            }
        }
        if self.allocated_bytes()? > self.idle_budget {
            memory::clear_cache().map_err(device_error)?;
        }
        if self.allocated_bytes()? > self.idle_budget {
            return Err(device_error(
                "idle allocation exceeded its resident reservation",
            ));
        }
        Ok(())
    }

    fn evaluate_step(
        &mut self,
        tokens: &[u32],
        sampling: Option<&mut Sampling>,
    ) -> Result<Option<u32>> {
        if tokens.iter().any(|token| *token >= 262144) {
            return Err(device_error("invalid input token"));
        }
        memory::reset_peak_memory().map_err(device_error)?;
        let ids: Vec<_> = tokens.iter().map(|token| *token as i32).collect();
        let output = if sampling.is_some() {
            Logits::Last
        } else {
            Logits::None
        };
        let logits = self
            .model
            .forward(&ids, &mut self.cache, output)
            .map_err(device_error)?;
        let Some(sampling) = sampling else {
            self.cache.evaluate().map_err(device_error)?;
            return self.completed(None);
        };
        let logits = logits.ok_or_else(|| device_error("missing requested logits"))?;
        let logits = logits.index((0, -1, ..));
        if sampling.temperature == 0.0 && sampling.constraint.is_none() {
            let token = argmax(&logits, None).map_err(device_error)?;
            let finite = logits
                .is_finite()
                .and_then(|values| values.all(None))
                .map_err(device_error)?;
            self.cache
                .evaluate_with(&[&token, &finite])
                .map_err(device_error)?;
            if !finite.try_item_exact::<bool>().map_err(device_error)? {
                return Err(device_error("non-finite model logits"));
            }
            self.completed(Some(token.try_item_cast::<u32>().map_err(device_error)?))
        } else {
            let logits = logits.as_dtype(Dtype::Float32).map_err(device_error)?;
            self.cache.evaluate_with(&[&logits]).map_err(device_error)?;
            self.completed(Some(sampling.sample(logits.as_slice::<f32>())?))
        }
    }

    fn completed(&self, token: Option<u32>) -> Result<Option<u32>> {
        if self.allocated_bytes()? > self.budget
            || memory::peak_memory().map_err(device_error)? > self.budget
        {
            return Err(device_error("execution exceeded its memory reservation"));
        }
        Ok(token)
    }

    pub fn allocated_bytes(&self) -> Result<usize> {
        Ok(memory::active_memory().map_err(device_error)?
            + memory::cache_memory().map_err(device_error)?)
    }

    pub fn cache_bytes(&self) -> usize {
        self.cache.bytes() + self.prefix.as_ref().map_or(0, Cache::bytes)
    }
}

impl Backend for Mlx {
    fn plan(directory: &Path, context: usize, chunk: usize) -> Result<crate::resources::Plan> {
        let config =
            super::config::read(directory).map_err(|e| Error::Checkpoint(e.to_string()))?;
        super::layout::validate(
            &directory.join("model.safetensors"),
            &config["quantization"],
        )
        .map_err(|e| Error::Checkpoint(e.to_string()))?;
        let bits = super::weights::quantization_bits(
            &config["quantization"],
            "model.embed_tokens_per_layer",
        )
        .map_err(|e| Error::Checkpoint(e.to_string()))?;
        super::ple::MappedPLE::open(&directory.join("model.safetensors"), bits)
            .map_err(|e| Error::Checkpoint(e.to_string()))?;
        let (weights, mapped) =
            crate::tensor_inventory::safetensors(&directory.join("model.safetensors"))?;
        crate::resources::Plan::e4b(weights, mapped, context, chunk, true)
    }
    fn forward(&mut self, tokens: &[u32], sampling: Option<&mut Sampling>) -> Result<Option<u32>> {
        let token = self.evaluate_step(tokens, sampling)?;
        self.quiescent()?;
        Ok(token)
    }

    fn reset(&mut self) -> Result<()> {
        self.cache = Cache::default();
        self.quiescent()
    }

    fn save_prefix(&mut self) -> Result<()> {
        self.cache.evaluate().map_err(device_error)?;
        self.prefix = Some(self.cache.clone());
        Ok(())
    }

    fn restore_prefix(&mut self) -> Result<()> {
        self.cache = self
            .prefix
            .as_ref()
            .ok_or_else(|| device_error("missing prefix checkpoint"))?
            .clone();
        Ok(())
    }

    fn release(self) -> Result<()> {
        drop(self);
        memory::clear_cache().map_err(device_error)?;
        memory::set_wired_limit(0).map_err(device_error)?;
        Ok(())
    }

    fn clear_prefix(&mut self) -> Result<()> {
        self.prefix = None;
        self.quiescent()
    }
}
