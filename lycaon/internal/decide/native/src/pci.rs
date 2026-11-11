use std::fmt;
use std::str::FromStr;

use crate::error::{Error, Result};

/// A PCI address independent of backend enumeration order.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PciAddress {
    domain: u32,
    bus: u8,
    slot: u8,
    function: u8,
}

impl FromStr for PciAddress {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self> {
        let invalid = || Error::Device(format!("invalid PCI address {value:?}"));
        if !(12..=16).contains(&value.len()) {
            return Err(invalid());
        }
        let parts: Vec<_> = value.split([':', '.']).collect();
        if parts.len() != 4
            || !(4..=8).contains(&parts[0].len())
            || parts[1].len() != 2
            || parts[2].len() != 2
            || parts[3].len() != 1
            || parts.iter().any(|part| !part.bytes().all(|b| b.is_ascii_hexdigit()))
            || value.bytes().filter(|b| *b == b':').count() != 2
            || !value.rsplit_once('.').is_some_and(|(_, f)| f.len() == 1)
        {
            return Err(invalid());
        }
        let domain = u32::from_str_radix(parts[0], 16).map_err(|_| invalid())?;
        let bus = u8::from_str_radix(parts[1], 16).map_err(|_| invalid())?;
        let slot = u8::from_str_radix(parts[2], 16).map_err(|_| invalid())?;
        let function = u8::from_str_radix(parts[3], 16).map_err(|_| invalid())?;
        if slot > 31 || function > 7 {
            return Err(invalid());
        }
        Ok(Self { domain, bus, slot, function })
    }
}

impl PciAddress {
    #[cfg(feature = "vulkan")]
    pub(crate) fn open(self) -> Result<crate::device::Selected> {
        let gpu = crate::vulkan::gpus().iter().find(|gpu| {
            gpu.pci_address.as_deref().and_then(|address| address.parse::<Self>().ok()) == Some(self)
        });
        match gpu {
            Some(gpu) => Ok(crate::device::Selected::Vulkan(gpu.clone())),
            None => Err(Error::Device(format!("Vulkan GPU at {self} is unavailable"))),
        }
    }

    #[cfg(not(feature = "vulkan"))]
    pub(crate) fn open(self) -> Result<crate::device::Selected> {
        Err(Error::Device("Vulkan is unavailable in this build".into()))
    }
}

impl fmt::Display for PciAddress {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:04x}:{:02x}:{:02x}.{:x}", self.domain, self.bus, self.slot, self.function)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn canonical_addresses_and_bounds() {
        for (input, expected) in [
            ("0000:03:00.0", "0000:03:00.0"),
            ("00000000:AB:1F.7", "0000:ab:1f.7"),
            ("FFFFFFFF:ff:1f.7", "ffffffff:ff:1f.7"),
        ] {
            assert_eq!(input.parse::<PciAddress>().unwrap().to_string(), expected);
        }
        for bad in [
            "", "0:03:00.0", "0000:3:00.0", "0000:03:20.0", "0000:03:00.8",
            "0000.03:00:0", "0000:03.00.0", "0000:03:00.0 ", "0000:03:gg.0",
            "100000000:03:00.0", "0000:+3:00.0",
        ] {
            assert!(bad.parse::<PciAddress>().is_err(), "{bad}");
        }
    }

    #[test]
    fn physical_device_choice_round_trips() {
        let choice = "vulkan-pci:00000000:AB:1F.7".parse::<crate::DeviceChoice>().unwrap();
        assert_eq!(choice.to_string(), "vulkan-pci:0000:ab:1f.7");
        assert!("vulkan-pci:0000:03:00.8".parse::<crate::DeviceChoice>().is_err());
    }
}
