//! Smooth scrolling for notched mouse wheels.
//!
//! WebKit on macOS applies each notch at once and offers no embedder switch, so
//! the macOS host replays a notch over web content as a glide of pixel scroll
//! events that WebKit scrolls natively. Trackpad, Magic Mouse, and momentum
//! input pass through untouched.

pub mod glide;
#[cfg(target_os = "macos")]
mod macos;

#[cfg(target_os = "macos")]
pub use macos::install;
