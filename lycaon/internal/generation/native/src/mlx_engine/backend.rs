use super::{Cache, Model};
use crate::{
    error::{Error, Result},
    execution::{Backend, Sampling},
    sampler,
};
use mlx_rs::{Dtype, memory, ops::indexing::*};
use std::path::Path;

fn device_error(error: impl std::fmt::Display) -> Error {
    Error::Device(error.to_string())
}

pub struct Mlx {
    model: Model,
    cache: Cache,
    prefix: Option<Cache>,
}

impl Mlx {
    pub fn load(directory: &Path) -> Result<Self> {
        memory::set_cache_limit(128 << 20).map_err(device_error)?;
        Ok(Self {
            model: Model::load(directory).map_err(device_error)?,
            cache: Cache::default(),
            prefix: None,
        })
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
    fn forward(&mut self, tokens: &[u32], sampling: Option<&mut Sampling>) -> Result<Option<u32>> {
        if tokens.iter().any(|token| *token >= 262144) {
            return Err(device_error("invalid input token"));
        }
        let ids: Vec<_> = tokens.iter().map(|token| *token as i32).collect();
        let logits = self
            .model
            .forward(&ids, &mut self.cache)
            .map_err(device_error)?;
        let Some(sampling) = sampling else {
            self.cache.evaluate().map_err(device_error)?;
            return Ok(None);
        };
        let logits = logits.index((0, -1, ..));
        if sampling.temperature == 0.0 {
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
            Ok(Some(token.try_item_cast::<u32>().map_err(device_error)?))
        } else {
            let logits = logits.as_dtype(Dtype::Float32).map_err(device_error)?;
            self.cache.evaluate_with(&[&logits]).map_err(device_error)?;
            Ok(Some(sampler::sample(
                logits.as_slice::<f32>(),
                sampling.temperature,
                sampling.top_p,
                &mut sampling.rng,
            )?))
        }
    }

    fn reset(&mut self) {
        self.cache = Cache::default();
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

    fn clear_prefix(&mut self) {
        self.prefix = None;
    }
}
