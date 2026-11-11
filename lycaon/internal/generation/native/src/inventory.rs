use crate::{Result, runner::wire::PhysicalDevice};
use serde::Serialize;

#[derive(Serialize)]
pub struct Inventory {
    pub protocol: u32,
    pub backend: &'static str,
    pub devices: Vec<Device>,
}

#[derive(Serialize)]
pub struct Device {
    pub physical_device: PhysicalDevice,
    pub name: String,
    pub unified_memory: bool,
    pub total_bytes: u64,
    pub recommended_bytes: u64,
    pub available_bytes: Option<u64>,
}

#[cfg(all(feature = "mlx", target_os = "macos", target_arch = "aarch64"))]
#[path = "inventory_mlx.rs"]
mod platform;
#[cfg(all(feature = "vulkan", target_os = "linux"))]
#[path = "inventory_vulkan.rs"]
mod platform;

#[cfg(any(
    all(feature = "mlx", target_os = "macos", target_arch = "aarch64"),
    all(feature = "vulkan", target_os = "linux")
))]
pub fn inspect() -> Result<Inventory> {
    platform::inspect()
}

#[cfg(not(any(
    all(feature = "mlx", target_os = "macos", target_arch = "aarch64"),
    all(feature = "vulkan", target_os = "linux")
)))]
pub fn inspect() -> Result<Inventory> {
    Err(crate::Error::Device(
        "no generation adapter was built".into(),
    ))
}
