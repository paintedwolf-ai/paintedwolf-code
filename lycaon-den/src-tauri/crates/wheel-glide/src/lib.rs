//! Notched wheel input becomes native pixel glides over web content.
//! Precise scrolling and momentum events pass through unchanged.

pub mod glide;
#[cfg(target_os = "macos")]
mod events;
#[cfg(target_os = "macos")]
mod macos;

#[cfg(target_os = "macos")]
pub use macos::install;
