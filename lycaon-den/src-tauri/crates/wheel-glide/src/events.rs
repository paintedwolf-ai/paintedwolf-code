//! Converts pixel glides to native scroll events on the main thread.

use objc2::{rc::Retained, MainThreadMarker};
use objc2_app_kit::NSEvent;
use objc2_core_foundation::CGPoint;
use objc2_core_graphics::{CGEvent, CGEventField, CGEventFlags, CGScrollEventUnit};
use objc2_foundation::NSPoint;

/// Source user data stamped on replayed events.
pub(crate) const SYNTHETIC_TAG: i64 = 0x5057_474c_4944;

/// Fresh pixel events retain modifiers without carrying hardware notch fields.
pub(crate) fn replay(
    _mtm: MainThreadMarker,
    flags: CGEventFlags,
    point: NSPoint,
    dx: i32,
    dy: i32,
) -> Option<Retained<NSEvent>> {
    let event = CGEvent::new_scroll_wheel_event2(None, CGScrollEventUnit::Pixel, 2, dy, dx, 0)?;
    CGEvent::set_integer_value_field(Some(&event), CGEventField::ScrollWheelEventIsContinuous, 1);
    CGEvent::set_integer_value_field(Some(&event), CGEventField::EventSourceUserData, SYNTHETIC_TAG);
    CGEvent::set_flags(Some(&event), flags);
    CGEvent::set_timestamp(Some(&event), uptime_ns());
    aim(&event, point)?;
    NSEvent::eventWithCGEvent(&event)
}

/// Uses the uptime clock shared by events and display links.
fn uptime_ns() -> u64 {
    let mut now = libc::timespec { tv_sec: 0, tv_nsec: 0 };
    // SAFETY: a valid clock id and a live out-pointer.
    unsafe { libc::clock_gettime(libc::CLOCK_UPTIME_RAW, &mut now) };
    now.tv_sec as u64 * 1_000_000_000 + now.tv_nsec as u64
}

pub(crate) fn uptime_s() -> f64 {
    uptime_ns() as f64 / 1e9
}

/// Two probes invert AppKit's screen-dependent mapping to window coordinates.
fn aim(event: &CGEvent, point: NSPoint) -> Option<()> {
    let seen_at = |x: f64, y: f64| {
        CGEvent::set_location(Some(event), CGPoint::new(x, y));
        NSEvent::eventWithCGEvent(event).map(|probe| probe.locationInWindow())
    };
    let origin = seen_at(0.0, 0.0)?;
    let unit = seen_at(1.0, 1.0)?;
    let (scale_x, scale_y) = (unit.x - origin.x, unit.y - origin.y);
    if scale_x == 0.0 || scale_y == 0.0 {
        return None;
    }
    CGEvent::set_location(
        Some(event),
        CGPoint::new((point.x - origin.x) / scale_x, (point.y - origin.y) / scale_y),
    );
    Some(())
}
