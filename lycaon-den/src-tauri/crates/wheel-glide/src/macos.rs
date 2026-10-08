//! Replays notched wheel input as pixel scroll glides in its target web view.

use std::cell::RefCell;
use std::ptr::{self, NonNull};
use std::rc::Rc;

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::AnyObject;
use objc2::{define_class, msg_send, sel, MainThreadMarker, MainThreadOnly};
use objc2_app_kit::{
    NSEvent, NSEventMask, NSEventModifierFlags, NSEventPhase, NSEventType, NSWindow, NSWorkspace,
};
use objc2_core_foundation::CGPoint;
use objc2_core_graphics::{CGEvent, CGEventField, CGEventFlags, CGScrollEventUnit};
use objc2_foundation::{NSObject, NSObjectProtocol, NSPoint, NSRunLoop, NSRunLoopCommonModes};
use objc2_quartz_core::CADisplayLink;
use objc2_web_kit::WKWebView;

use crate::glide::{route, GlideReport, Glides, Origin, Vector, WheelFacts, WheelRoute, PIXELS_PER_LINE};

/// Source user data stamped on replayed events.
const SYNTHETIC_TAG: i64 = 0x5057_474c_4944;

/// What a glide replays into: the web view under the notch, latched for the
/// whole glide, the notch's point in window coordinates, and its modifiers.
struct Template {
    web_view: Retained<WKWebView>,
    point: NSPoint,
    flags: CGEventFlags,
}

define_class!(
    // SAFETY: NSObject has no subclassing requirements and `Pacer` has no Drop.
    #[unsafe(super(NSObject))]
    #[thread_kind = MainThreadOnly]
    #[name = "DenWheelGlidePacer"]
    struct Pacer;

    impl Pacer {
        #[unsafe(method(step:))]
        fn step(&self, link: &CADisplayLink) {
            // Positions are taken for the moment the frame reaches the screen.
            frame(link.targetTimestamp());
        }
    }

    unsafe impl NSObjectProtocol for Pacer {}
);

impl Pacer {
    fn new(mtm: MainThreadMarker) -> Retained<Self> {
        // SAFETY: NSObject's designated initializer.
        unsafe { msg_send![Self::alloc(mtm), init] }
    }
}

struct State {
    glides: Glides<Template>,
    pace: Option<Retained<CADisplayLink>>,
    report: Rc<dyn Fn(GlideReport)>,
    _monitor: Retained<AnyObject>,
}

impl State {
    fn stop_pace(&mut self) {
        if let Some(pace) = self.pace.take() {
            pace.invalidate();
        }
    }
}

thread_local! {
    static STATE: RefCell<Option<State>> = const { RefCell::new(None) };
}

/// Installs the wheel monitor for every window of the process.
pub fn install(report: Rc<dyn Fn(GlideReport)>) -> Result<(), String> {
    let mtm = MainThreadMarker::new().ok_or("wheel smoothing installs on the main thread")?;
    let mask = NSEventMask::ScrollWheel
        | NSEventMask::LeftMouseDown
        | NSEventMask::RightMouseDown
        | NSEventMask::OtherMouseDown
        | NSEventMask::KeyDown;
    let handler = RcBlock::new(move |event: NonNull<NSEvent>| -> *mut NSEvent {
        // SAFETY: AppKit passes a live event for the duration of the handler.
        if handle(unsafe { event.as_ref() }, mtm) {
            event.as_ptr()
        } else {
            ptr::null_mut()
        }
    });
    // SAFETY: the handler returns the event it received or null.
    let monitor = unsafe { NSEvent::addLocalMonitorForEventsMatchingMask_handler(mask, &handler) }
        .ok_or("AppKit refused the wheel monitor")?;
    STATE.with(|cell| {
        *cell.borrow_mut() = Some(State {
            glides: Glides::default(),
            pace: None,
            report,
            _monitor: monitor,
        });
    });
    Ok(())
}

/// Returns whether AppKit should keep dispatching `event`.
fn handle(event: &NSEvent, mtm: MainThreadMarker) -> bool {
    if event.r#type() != NSEventType::ScrollWheel {
        interrupt();
        return true;
    }
    let Some(cg) = event.CGEvent() else {
        return true;
    };
    if CGEvent::integer_value_field(Some(&cg), CGEventField::EventSourceUserData) == SYNTHETIC_TAG {
        return true;
    }
    let precise = event.hasPreciseScrollingDeltas();
    if precise {
        let facts = WheelFacts {
            precise,
            touch_began: event
                .phase()
                .intersects(NSEventPhase::Began | NSEventPhase::MayBegin),
            ..WheelFacts::default()
        };
        if route(&facts) == WheelRoute::PassAndInterrupt {
            interrupt();
        }
        return true;
    }
    let window = event.window(mtm);
    let point = event.locationInWindow();
    let web_view = window.as_deref().and_then(|window| web_view_at(window, point));
    let facts = WheelFacts {
        precise,
        touch_began: false,
        zoom_modifier: event.modifierFlags().intersects(
            NSEventModifierFlags::Control | NSEventModifierFlags::Option | NSEventModifierFlags::Command,
        ),
        reduce_motion: NSWorkspace::sharedWorkspace().accessibilityDisplayShouldReduceMotion(),
        over_web_content: web_view.is_some(),
    };
    if route(&facts) != WheelRoute::Glide {
        return true;
    }
    // WebKit applies a notch as its line delta times the line step.
    let delta = Vector {
        x: event.deltaX() * PIXELS_PER_LINE,
        y: event.deltaY() * PIXELS_PER_LINE,
    };
    let (Some(window), Some(web_view)) = (window, web_view) else {
        return true;
    };
    if delta == Vector::default() {
        return true;
    }
    let origin = Origin { window: window.windowNumber(), x: point.x, y: point.y };
    let flags = CGEvent::flags(Some(&cg));
    !begin_notch(origin, delta, Template { web_view, point, flags }, mtm)
}

fn web_view_at(window: &NSWindow, point: NSPoint) -> Option<Retained<WKWebView>> {
    let content = window.contentView()?;
    // `hitTest:` takes a point in the receiver's superview coordinates.
    // SAFETY: views are read on the main thread during event dispatch.
    let local = unsafe { content.superview() }
        .map_or(point, |frame| frame.convertPoint_fromView(point, None));
    let mut view = content.hitTest(local);
    while let Some(candidate) = view {
        match candidate.downcast::<WKWebView>() {
            Ok(web_view) => return Some(web_view),
            // SAFETY: as above.
            Err(candidate) => view = unsafe { candidate.superview() },
        }
    }
    None
}

/// Returns whether the notch joined a glide; the caller consumes it if so.
fn begin_notch(origin: Origin, delta: Vector, template: Template, mtm: MainThreadMarker) -> bool {
    STATE.with(|cell| {
        let Ok(mut guard) = cell.try_borrow_mut() else {
            return false;
        };
        let Some(state) = guard.as_mut() else {
            return false;
        };
        if state.pace.is_none() {
            state.pace = Some(start_pace(&template.web_view, mtm));
        }
        state.glides.notch(origin, delta, uptime_s(), template);
        true
    })
}

/// Paces glides by the display under `web_view`; concurrent glides in other
/// windows share it.
fn start_pace(web_view: &WKWebView, mtm: MainThreadMarker) -> Retained<CADisplayLink> {
    let pacer = Pacer::new(mtm);
    // SAFETY: `step:` takes the display link, as the selector requires.
    let link = unsafe { web_view.displayLinkWithTarget_selector(&pacer, sel!(step:)) };
    // SAFETY: the main run loop, on the main thread. Common modes keep a
    // glide running while menus and drags track events.
    unsafe { link.addToRunLoop_forMode(&NSRunLoop::mainRunLoop(), NSRunLoopCommonModes) };
    link
}

/// Advances every glide to `now`, on the uptime clock.
fn frame(now: f64) {
    let mut sends: Vec<(Retained<WKWebView>, NSPoint, CGEventFlags, i32, i32)> = Vec::new();
    let finished = STATE.with(|cell| {
        let mut guard = cell.try_borrow_mut().ok()?;
        let state = guard.as_mut()?;
        let reports = state.glides.tick(now, |template, dx, dy| {
            sends.push((template.web_view.clone(), template.point, template.flags, dx, dy));
        });
        if state.glides.is_idle() {
            state.stop_pace();
        }
        Some((reports, Rc::clone(&state.report)))
    });
    // Deliver outside the state borrow: delivery can run page script.
    for (web_view, point, flags, dx, dy) in sends {
        if !web_view.window().is_some_and(|window| window.isVisible()) {
            continue;
        }
        if let Some(replay) = replay(flags, point, dx, dy) {
            web_view.scrollWheel(&replay);
        }
    }
    if let Some((reports, report)) = finished {
        reports.into_iter().for_each(|entry| report(entry));
    }
}

fn interrupt() {
    let finished = STATE.with(|cell| {
        let mut guard = cell.try_borrow_mut().ok()?;
        let state = guard.as_mut()?;
        if state.glides.is_idle() {
            return None;
        }
        let reports = state.glides.interrupt(uptime_s());
        state.stop_pace();
        Some((reports, Rc::clone(&state.report)))
    });
    if let Some((reports, report)) = finished {
        reports.into_iter().for_each(|entry| report(entry));
    }
}

/// A continuous scroll event of `dx`/`dy` points at `point` in window
/// coordinates. Built fresh rather than copied from the notch, so no hardware
/// field outlives the conversion; only the modifiers carry over.
fn replay(flags: CGEventFlags, point: NSPoint, dx: i32, dy: i32) -> Option<Retained<NSEvent>> {
    let event = CGEvent::new_scroll_wheel_event2(None, CGScrollEventUnit::Pixel, 2, dy, dx, 0)?;
    CGEvent::set_integer_value_field(Some(&event), CGEventField::ScrollWheelEventIsContinuous, 1);
    CGEvent::set_integer_value_field(Some(&event), CGEventField::EventSourceUserData, SYNTHETIC_TAG);
    CGEvent::set_flags(Some(&event), flags);
    CGEvent::set_timestamp(Some(&event), uptime_ns());
    aim(&event, point)?;
    NSEvent::eventWithCGEvent(&event)
}

/// Nanoseconds since boot: the clock event timestamps and display link
/// timestamps both count on.
fn uptime_ns() -> u64 {
    let mut now = libc::timespec { tv_sec: 0, tv_nsec: 0 };
    // SAFETY: a valid clock id and a live out-pointer.
    unsafe { libc::clock_gettime(libc::CLOCK_UPTIME_RAW, &mut now) };
    now.tv_sec as u64 * 1_000_000_000 + now.tv_nsec as u64
}

fn uptime_s() -> f64 {
    uptime_ns() as f64 / 1e9
}

/// Sets the CG location whose windowless `locationInWindow` is `point`.
///
/// AppKit derives a windowless event's location from its CG location through
/// a display flip whose base depends on the screen layout, so measure the
/// mapping with two throwaway events and invert it.
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn glides_count_time_on_the_display_link_clock() {
        let before = objc2_quartz_core::CACurrentMediaTime();
        let uptime = uptime_s();
        let after = objc2_quartz_core::CACurrentMediaTime();

        assert!(
            uptime >= before - 1e-3 && uptime <= after + 1e-3,
            "uptime {uptime} s outside media time {before}..{after} s"
        );
    }

    #[test]
    fn a_replay_reads_as_precise_points_at_the_notch_point() {
        let replay = replay(CGEventFlags::empty(), NSPoint::new(120.0, 340.0), 5, -12)
            .expect("replayed event");

        assert!(replay.hasPreciseScrollingDeltas());
        assert_eq!(replay.scrollingDeltaY(), -12.0);
        assert_eq!(replay.scrollingDeltaX(), 5.0);
        assert!(replay.phase().is_empty());
        assert!(replay.momentumPhase().is_empty());
        assert_eq!(replay.locationInWindow(), NSPoint::new(120.0, 340.0));
    }

    #[test]
    fn a_replay_lands_on_its_point_anywhere_on_the_desktop() {
        for point in [
            NSPoint::new(0.0, 0.0),
            NSPoint::new(2560.5, 1410.25),
            NSPoint::new(-1512.0, -982.0),
        ] {
            let replay = replay(CGEventFlags::empty(), point, 0, -4).expect("replayed event");
            assert_eq!(replay.locationInWindow(), point);
        }
    }

    #[test]
    fn a_replay_keeps_the_notch_modifiers() {
        let replay = replay(CGEventFlags::MaskShift, NSPoint::new(0.0, 0.0), 0, 3)
            .expect("replayed event");

        assert!(replay.modifierFlags().contains(NSEventModifierFlags::Shift));
    }

    #[test]
    fn a_replay_is_tagged_and_stamped_now() {
        let replay = replay(CGEventFlags::empty(), NSPoint::new(0.0, 0.0), 0, 3)
            .expect("replayed event");
        let cg = replay.CGEvent().expect("backing CGEvent");

        assert_eq!(
            CGEvent::integer_value_field(Some(&cg), CGEventField::EventSourceUserData),
            SYNTHETIC_TAG
        );
        let now_s = uptime_s();
        assert!(
            (replay.timestamp() - now_s).abs() < 0.5,
            "replay stamped {} s, uptime {now_s} s",
            replay.timestamp()
        );
    }
}
