use crate::{
    error::{Error, Result},
    execution::{Backend, Sampling},
    sampler,
};
use std::{
    ffi::{CStr, CString, c_char, c_int, c_void},
    path::Path,
    ptr::NonNull,
};

#[repr(C)]
#[derive(Default)]
pub struct DeviceInfo {
    pub total_bytes: u64,
    pub free_bytes: u64,
}

unsafe extern "C" {
    fn pw_generation_probe(
        pci: *const c_char,
        info: *mut DeviceInfo,
        error: *mut c_char,
        capacity: usize,
    ) -> c_int;
    fn pw_generation_load(
        model: *const c_char,
        pci: *const c_char,
        context: u32,
        chunk: u32,
        prefix: u32,
        engine: *mut *mut c_void,
        error: *mut c_char,
        capacity: usize,
    ) -> c_int;
    fn pw_generation_forward(
        engine: *mut c_void,
        tokens: *const u32,
        count: usize,
        output: c_int,
        logits: *mut f32,
        sampled: *mut u32,
        error: *mut c_char,
        capacity: usize,
    ) -> c_int;
    fn pw_generation_cache(
        engine: *mut c_void,
        operation: c_int,
        error: *mut c_char,
        capacity: usize,
    ) -> c_int;
    fn pw_generation_free(engine: *mut c_void);
}

fn checked(operation: impl FnOnce(*mut c_char, usize) -> c_int) -> Result<()> {
    let mut error = [0 as c_char; 1024];
    if operation(error.as_mut_ptr(), error.len()) == 0 {
        return Ok(());
    }
    let message = unsafe { CStr::from_ptr(error.as_ptr()) }.to_string_lossy();
    Err(Error::Device(format!("Vulkan: {message}")))
}

fn string(value: &[u8]) -> Result<CString> {
    CString::new(value).map_err(|_| Error::Protocol("invalid native string".into()))
}

pub struct Vulkan {
    engine: NonNull<c_void>,
    logits: Vec<f32>,
}

impl Vulkan {
    pub fn probe(pci: &str) -> Result<DeviceInfo> {
        let pci = string(pci.as_bytes())?;
        let mut info = DeviceInfo::default();
        checked(|error, capacity| unsafe {
            pw_generation_probe(pci.as_ptr(), &mut info, error, capacity)
        })?;
        Ok(info)
    }

    pub fn load(directory: &Path, pci: &str, context: usize, chunk: usize) -> Result<Self> {
        use std::os::unix::ffi::OsStrExt;
        let path = string(directory.join("model.gguf").as_os_str().as_bytes())?;
        let pci = string(pci.as_bytes())?;
        let mut engine = std::ptr::null_mut();
        checked(|error, capacity| unsafe {
            pw_generation_load(
                path.as_ptr(),
                pci.as_ptr(),
                context as u32,
                chunk as u32,
                4096.min(context.saturating_sub(1)) as u32,
                &mut engine,
                error,
                capacity,
            )
        })?;
        Ok(Self {
            engine: NonNull::new(engine)
                .ok_or_else(|| Error::Device("missing Vulkan engine".into()))?,
            logits: vec![0.0; 262144],
        })
    }

    fn cache(&mut self, operation: c_int) -> Result<()> {
        checked(|error, capacity| unsafe {
            pw_generation_cache(self.engine.as_ptr(), operation, error, capacity)
        })
    }
}

impl Backend for Vulkan {
    fn forward(&mut self, tokens: &[u32], sampling: Option<&mut Sampling>) -> Result<Option<u32>> {
        let mut sampled = u32::MAX;
        let mode = sampling
            .as_ref()
            .map_or(0, |s| if s.temperature == 0.0 { 2 } else { 1 });
        checked(|error, capacity| unsafe {
            pw_generation_forward(
                self.engine.as_ptr(),
                tokens.as_ptr(),
                tokens.len(),
                mode,
                self.logits.as_mut_ptr(),
                &mut sampled,
                error,
                capacity,
            )
        })?;
        if sampled != u32::MAX {
            return Ok(Some(sampled));
        }
        sampling
            .map(|sampling| {
                sampler::sample(
                    &self.logits,
                    sampling.temperature,
                    sampling.top_p,
                    sampling.top_k,
                    &mut sampling.rng,
                )
            })
            .transpose()
    }
    fn reset(&mut self) -> Result<()> {
        self.cache(0)
    }
    fn save_prefix(&mut self) -> Result<()> {
        self.cache(1)
    }
    fn restore_prefix(&mut self) -> Result<()> {
        self.cache(2)
    }
    fn clear_prefix(&mut self) -> Result<()> {
        self.cache(3)
    }
}

impl Drop for Vulkan {
    fn drop(&mut self) {
        unsafe { pw_generation_free(self.engine.as_ptr()) };
    }
}
