use super::{Device, Inventory};
use crate::{Error, Result, runner::wire::PhysicalDevice};
use std::ffi::{CStr, c_char, c_int};

#[repr(C)]
struct RawDevice {
    pci: [c_char; 32],
    name: [c_char; 256],
    total_bytes: u64,
    available_bytes: u64,
    unified_memory: u32,
    availability_known: u32,
}

unsafe extern "C" {
    fn pw_generation_inventory(
        devices: *mut RawDevice,
        capacity: usize,
        count: *mut usize,
        error: *mut c_char,
        error_capacity: usize,
    ) -> c_int;
}

pub fn inspect() -> Result<Inventory> {
    let mut raw: [RawDevice; 32] = unsafe { std::mem::zeroed() };
    let mut count = 0;
    let mut error = [0 as c_char; 1024];
    let status = unsafe {
        pw_generation_inventory(
            raw.as_mut_ptr(),
            raw.len(),
            &mut count,
            error.as_mut_ptr(),
            error.len(),
        )
    };
    if status != 0 || count > raw.len() {
        let message = unsafe { CStr::from_ptr(error.as_ptr()) }.to_string_lossy();
        return Err(Error::Device(format!("Vulkan inventory: {message}")));
    }
    let devices = raw[..count]
        .iter()
        .map(|device| {
            let address = unsafe { CStr::from_ptr(device.pci.as_ptr()) }
                .to_string_lossy()
                .into_owned();
            let name = unsafe { CStr::from_ptr(device.name.as_ptr()) }
                .to_string_lossy()
                .into_owned();
            Device {
                physical_device: PhysicalDevice::Pci { address },
                name,
                unified_memory: device.unified_memory != 0,
                total_bytes: device.total_bytes,
                recommended_bytes: device.total_bytes,
                available_bytes: (device.availability_known != 0).then_some(device.available_bytes),
            }
        })
        .collect();
    Ok(Inventory {
        protocol: 1,
        backend: "vulkan",
        devices,
    })
}
