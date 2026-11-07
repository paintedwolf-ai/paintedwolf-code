use super::{Device, Inventory};
use crate::{Error, Result, runner::wire::PhysicalDevice};
use std::ffi::CStr;

struct Handles {
    device: mlx_sys::mlx_device,
    info: mlx_sys::mlx_device_info,
}

impl Drop for Handles {
    fn drop(&mut self) {
        unsafe {
            mlx_sys::mlx_device_info_free(self.info);
            mlx_sys::mlx_device_free(self.device);
        }
    }
}

impl Handles {
    fn size(&self, key: &CStr) -> Result<u64> {
        let mut value = 0;
        let status =
            unsafe { mlx_sys::mlx_device_info_get_size(&mut value, self.info, key.as_ptr()) };
        if status != 0 || value == 0 {
            return Err(Error::Device(format!("missing Metal property {key:?}")));
        }
        Ok(value as u64)
    }
}

pub fn inspect() -> Result<Inventory> {
    let mut handles = Handles {
        device: unsafe { mlx_sys::mlx_device_new_type(mlx_sys::mlx_device_type__MLX_GPU, 0) },
        info: unsafe { mlx_sys::mlx_device_info_new() },
    };
    if unsafe { mlx_sys::mlx_device_info_get(&mut handles.info, handles.device) } != 0 {
        return Err(Error::Device("Metal inventory is unavailable".into()));
    }
    let mut name = std::ptr::null();
    if unsafe {
        mlx_sys::mlx_device_info_get_string(&mut name, handles.info, c"device_name".as_ptr())
    } != 0
        || name.is_null()
    {
        return Err(Error::Device("Metal device name is unavailable".into()));
    }
    let name = unsafe { CStr::from_ptr(name) }
        .to_string_lossy()
        .into_owned();
    let total_bytes = handles.size(c"memory_size")?;
    let recommended_bytes = handles
        .size(c"max_recommended_working_set_size")?
        .min(total_bytes);
    Ok(Inventory {
        protocol: 1,
        backend: "mlx",
        devices: vec![Device {
            physical_device: PhysicalDevice::AppleSilicon,
            name,
            unified_memory: true,
            total_bytes,
            recommended_bytes,
            available_bytes: None,
        }],
    })
}
