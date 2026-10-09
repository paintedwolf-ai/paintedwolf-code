#[cfg(target_os = "macos")]
#[path = "../src/events.rs"]
mod events;

#[cfg(target_os = "macos")]
mod native {
    use super::events::{replay, uptime_s, SYNTHETIC_TAG};
    use objc2::{rc::autoreleasepool, MainThreadMarker};
    use objc2_app_kit::{NSApplication, NSEventModifierFlags};
    use objc2_core_graphics::{CGEvent, CGEventField, CGEventFlags};
    use objc2_foundation::NSPoint;

    pub(super) fn run() {
        let mtm = MainThreadMarker::new().expect("native event tests run on the main thread");
        autoreleasepool(|_| {
            let _app = NSApplication::sharedApplication(mtm);
            glides_count_time_on_the_display_link_clock(mtm);
            a_replay_reads_as_precise_points_at_the_notch_point(mtm);
            a_replay_lands_on_its_point_anywhere_on_the_desktop(mtm);
            a_replay_keeps_the_notch_modifiers(mtm);
            a_replay_is_tagged_and_stamped_now(mtm);
        });
        println!("native event checks: 5 passed");
    }

    fn glides_count_time_on_the_display_link_clock(_mtm: MainThreadMarker) {
        let before = objc2_quartz_core::CACurrentMediaTime();
        let uptime = uptime_s();
        let after = objc2_quartz_core::CACurrentMediaTime();

        assert!(
            uptime >= before - 1e-3 && uptime <= after + 1e-3,
            "uptime {uptime} s outside media time {before}..{after} s"
        );
    }

    fn a_replay_reads_as_precise_points_at_the_notch_point(mtm: MainThreadMarker) {
        let replay = replay(mtm, CGEventFlags::empty(), NSPoint::new(120.0, 340.0), 5, -12)
            .expect("replayed event");

        assert!(replay.hasPreciseScrollingDeltas());
        assert_eq!(replay.scrollingDeltaY(), -12.0);
        assert_eq!(replay.scrollingDeltaX(), 5.0);
        assert!(replay.phase().is_empty());
        assert!(replay.momentumPhase().is_empty());
        assert_eq!(replay.locationInWindow(), NSPoint::new(120.0, 340.0));
    }

    fn a_replay_lands_on_its_point_anywhere_on_the_desktop(mtm: MainThreadMarker) {
        for point in [
            NSPoint::new(0.0, 0.0),
            NSPoint::new(2560.5, 1410.25),
            NSPoint::new(-1512.0, -982.0),
        ] {
            let replay = replay(mtm, CGEventFlags::empty(), point, 0, -4).expect("replayed event");
            assert_eq!(replay.locationInWindow(), point);
        }
    }

    fn a_replay_keeps_the_notch_modifiers(mtm: MainThreadMarker) {
        let replay = replay(mtm, CGEventFlags::MaskShift, NSPoint::new(0.0, 0.0), 0, 3)
            .expect("replayed event");

        assert!(replay.modifierFlags().contains(NSEventModifierFlags::Shift));
    }

    fn a_replay_is_tagged_and_stamped_now(mtm: MainThreadMarker) {
        let replay = replay(mtm, CGEventFlags::empty(), NSPoint::new(0.0, 0.0), 0, 3)
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

fn main() {
    #[cfg(target_os = "macos")]
    native::run();
}
