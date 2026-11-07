use crate::{
    device::Selected,
    error::{Error, Result},
};

#[derive(Clone, Copy, Debug, Default)]
pub struct Limits {
    pub resident: usize,
    pub peak: usize,
}

impl Limits {
    pub(crate) fn configure(self, selected: &Selected) -> Result<()> {
        if self.resident == 0 && self.peak == 0 {
            return Ok(());
        }
        if self.resident == 0 || self.peak <= self.resident {
            return Err(Error::Device("invalid memory limits".into()));
        }
        #[cfg(feature = "mlx")]
        if matches!(selected, Selected::Mlx) {
            mlx_rs::memory::set_memory_limit(self.peak)?;
            return self.begin();
        }
        let _ = selected;
        Err(Error::Device("this adapter cannot share transient workspace".into()))
    }

    pub(crate) fn begin(self) -> Result<()> {
        #[cfg(feature = "mlx")]
        if self.peak != 0 {
            mlx_rs::memory::reset_peak_memory()?;
        }
        Ok(())
    }

    pub(crate) fn finish(self) -> Result<()> {
        #[cfg(feature = "mlx")]
        if self.peak != 0 {
            use mlx_rs::memory;
            if memory::peak_memory()? > self.peak {
                return Err(Error::Device("execution exceeded its memory reservation".into()));
            }
            if memory::active_memory()? + memory::cache_memory()? > self.resident {
                memory::clear_cache()?;
            }
            if memory::active_memory()? + memory::cache_memory()? > self.resident {
                return Err(Error::Device(
                    "idle allocation exceeded its resident reservation".into(),
                ));
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sharing_requires_a_supported_adapter_and_complete_limits() {
        assert!(Limits::default().configure(&Selected::Cpu).is_ok());
        for (resident, peak) in [(0, 1), (1, 0), (1, 1), (2, 1), (1, 2)] {
            assert!(Limits { resident, peak }.configure(&Selected::Cpu).is_err());
        }
    }
}
