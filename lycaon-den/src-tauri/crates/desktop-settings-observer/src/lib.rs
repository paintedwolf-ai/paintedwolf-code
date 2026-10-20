//! Bounded, observation-only protocol for the separate Linux Settings process.
use serde::{Deserialize, Serialize};
use std::io::{self, BufRead, Read, Write};

pub const MAX_FRAME: usize = 16 * 1024;
pub const MAX_SETTINGS: usize = 6;
pub const NAMESPACES: [&str; 5] = [
    "org.freedesktop.appearance",
    "org.gnome.desktop.interface",
    "org.gnome.desktop.a11y.interface",
    "org.kde.kdeglobals.General",
    "org.gnome.desktop.wm.preferences",
];

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(
    rename_all = "snake_case",
    tag = "key",
    content = "value",
    deny_unknown_fields
)]
pub enum Setting {
    Contrast(u32),
    TextScale(f64),
    HighContrast(bool),
    Accent([f64; 3]),
    KdeAccent(String),
    ButtonLayout(String),
}
impl Setting {
    pub fn identity(&self) -> (&'static str, &'static str) {
        match self {
            Self::Contrast(_) => (NAMESPACES[0], "contrast"),
            Self::Accent(_) => (NAMESPACES[0], "accent-color"),
            Self::TextScale(_) => (NAMESPACES[1], "text-scaling-factor"),
            Self::HighContrast(_) => (NAMESPACES[2], "high-contrast"),
            Self::KdeAccent(_) => (NAMESPACES[3], "AccentColor"),
            Self::ButtonLayout(_) => (NAMESPACES[4], "button-layout"),
        }
    }
    pub fn valid(&self) -> bool {
        match self {
            Self::TextScale(v) => v.is_finite() && *v > 0.0,
            Self::Accent(v) => v.iter().all(|c| c.is_finite() && (0.0..=1.0).contains(c)),
            Self::KdeAccent(v) => v.len() <= 32 && !v.contains('\0'),
            Self::ButtonLayout(v) => v.len() <= 256 && !v.contains('\0'),
            _ => true,
        }
    }
}
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Status {
    Ready,
    Missing,
    Denied,
    Disconnected,
    TimedOut,
    Failed,
}
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Frame {
    pub schema_version: u8,
    pub sequence: u64,
    pub status: Status,
    pub settings: Vec<Setting>,
}
impl Frame {
    pub fn validate(&self) -> io::Result<()> {
        let unique: std::collections::BTreeSet<_> =
            self.settings.iter().map(Setting::identity).collect();
        if self.schema_version != 1
            || self.sequence == 0
            || self.settings.len() > MAX_SETTINGS
            || unique.len() != self.settings.len()
            || !self.settings.iter().all(Setting::valid)
            || (self.status != Status::Ready && !self.settings.is_empty())
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid desktop settings frame",
            ));
        }
        Ok(())
    }
    pub fn write(&self, output: &mut impl Write) -> io::Result<()> {
        self.validate()?;
        let raw = serde_json::to_vec(self)?;
        if raw.len() + 1 > MAX_FRAME {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "oversized desktop settings frame",
            ));
        }
        output.write_all(&raw)?;
        output.write_all(b"\n")?;
        output.flush()
    }
}
pub fn read(input: &mut impl BufRead, previous: u64) -> io::Result<Option<Frame>> {
    let mut raw = Vec::new();
    let count = input
        .take((MAX_FRAME + 1) as u64)
        .read_until(b'\n', &mut raw)?;
    if count == 0 {
        return Ok(None);
    }
    if count > MAX_FRAME || raw.last() != Some(&b'\n') {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "unbounded desktop settings frame",
        ));
    }
    let frame: Frame = serde_json::from_slice(&raw)?;
    frame.validate()?;
    if frame.sequence
        != previous.checked_add(1).ok_or_else(|| {
            io::Error::new(
                io::ErrorKind::InvalidData,
                "desktop settings sequence overflow",
            )
        })?
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "out-of-order desktop settings frame",
        ));
    }
    Ok(Some(frame))
}
#[cfg(target_os = "linux")]
pub mod process;
#[cfg(test)]
mod tests;
