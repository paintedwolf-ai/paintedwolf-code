//! Native window identity, presentation, and system appearance.

pub(crate) mod item_windows;
pub(crate) mod linux_decorations;
pub(crate) mod linux_frame;
pub(crate) mod window_appearance;
pub(crate) mod window_backdrop;
pub(crate) mod system_appearance;
pub(crate) mod accessibility_text_size;
pub(crate) mod web_engine;
pub(crate) mod accessibility_announce;
#[cfg(any(target_os = "linux", test))]
pub(crate) mod linux_desktop_settings;
