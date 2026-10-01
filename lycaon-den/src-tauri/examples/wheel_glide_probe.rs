//! Drives notched wheel events through the host's wheel smoothing into a real
//! WKWebView on every attached display, for a document scroller and an inner
//! scroll container, and checks what the page saw: whole distance, spread over
//! frames, direction, no overscroll, and untouched precise input.
//!
//! Run on macOS with a window server: `cargo run --example wheel_glide_probe`.

#[cfg(target_os = "macos")]
fn main() {
    std::process::exit(macos::run());
}

#[cfg(not(target_os = "macos"))]
fn main() {
    eprintln!("wheel_glide_probe runs on macOS only");
}

#[cfg(target_os = "macos")]
#[path = "support/web_view_driver.rs"]
mod web_view_driver;

#[cfg(target_os = "macos")]
mod macos {
    use std::time::Duration;

    use objc2::MainThreadMarker;
    use objc2_app_kit::NSScreen;
    use objc2_core_graphics::CGScrollEventUnit;
    use objc2_foundation::{NSPoint, NSSize};

    use crate::web_view_driver::Driver;

    const START_Y: f64 = 4000.0;
    const TARGET: NSPoint = NSPoint::new(400.0, 300.0);
    const CONTENT: &str =
        r#"<div style="height:20000px;background:linear-gradient(#fff,#333)"></div>"#;
    const RECORD: &str = r#"window.samples = [];
scrollTarget.addEventListener("scroll", () => samples.push([performance.now(), scroller.scrollTop]), { passive: true });"#;

    fn document_page() -> String {
        format!(
            r#"<!doctype html><body style="margin:0">{CONTENT}<script>
window.scroller = document.scrollingElement; window.scrollTarget = window;
{RECORD}</script></body>"#
        )
    }

    /// Den scrolls inner containers; the document itself never scrolls.
    fn inner_page() -> String {
        format!(
            r#"<!doctype html><body style="margin:0;overflow:hidden">
<div id="s" style="position:fixed;inset:0;overflow:auto">{CONTENT}</div><script>
window.scroller = document.getElementById("s"); window.scrollTarget = scroller;
{RECORD}</script></body>"#
        )
    }

    pub fn run() -> i32 {
        let mtm = MainThreadMarker::new().expect("main thread");
        let probe = Driver::open(mtm, NSSize::new(800.0, 600.0), None);
        let mut failures = Vec::new();
        let screens = NSScreen::screens(mtm);
        for index in 0..screens.count() {
            let screen = screens.objectAtIndex(index);
            let origin = screen.frame().origin;
            probe
                .window
                .setFrameOrigin(NSPoint::new(origin.x + 80.0, origin.y + 80.0));
            for (name, page) in [("document", document_page()), ("inner", inner_page())] {
                probe.load_html(&page);
                scenarios(&probe, &format!("screen {index} {name}"), &mut failures);
            }
        }
        if failures.is_empty() {
            println!("PASS");
            return 0;
        }
        for failure in &failures {
            println!("FAIL: {failure}");
        }
        1
    }

    fn scenarios(probe: &Driver, label: &str, failures: &mut Vec<String>) {
        // Delivery the host relies on: a windowless replay handed to the web
        // view, and WebKit's own distance for an unsmoothed notch.
        scroll_to(probe, START_Y);
        probe.web.scrollWheel(&probe.wheel_event(CGScrollEventUnit::Pixel, -30, false, TARGET));
        probe.spin(Duration::from_millis(300));
        check(&format!("{label}: windowless replay"), &samples(probe), 30.0, false, failures);

        scroll_to(probe, START_Y);
        probe.web.scrollWheel(&probe.wheel_event(CGScrollEventUnit::Line, -1, true, TARGET));
        probe.spin(Duration::from_millis(300));
        check(&format!("{label}: native notch"), &samples(probe), 40.0, false, failures);

        scroll_to(probe, START_Y);
        let expected = probe.send_notch(-1, TARGET);
        probe.spin(Duration::from_millis(500));
        check(&format!("{label}: single notch"), &samples(probe), expected, true, failures);

        scroll_to(probe, START_Y);
        let mut expected = 0.0;
        for _ in 0..5 {
            expected += probe.send_notch(-1, TARGET);
            probe.spin(Duration::from_millis(30));
        }
        probe.spin(Duration::from_millis(500));
        check(&format!("{label}: burst of five"), &samples(probe), expected, true, failures);

        scroll_to(probe, 0.0);
        probe.send_notch(3, TARGET);
        probe.spin(Duration::from_millis(500));
        let lowest = samples(probe).iter().map(|sample| sample.1).fold(0.0, f64::min);
        if lowest < 0.0 {
            failures.push(format!("{label}: a notch at the top overscrolled"));
        }

        // The monitor must leave precise input alone. AppKit cannot route a
        // synthetic event onward from `sendEvent:` (it has no window number),
        // so this checks the monitor, not the scroll.
        let glides_before = probe.reports.borrow().len();
        let precise = probe.wheel_event(CGScrollEventUnit::Pixel, -30, true, TARGET);
        probe.app.sendEvent(&precise);
        probe.spin(Duration::from_millis(300));
        if probe.reports.borrow().len() != glides_before {
            failures.push(format!("{label}: precise input started a glide"));
        }
    }

    fn check(label: &str, samples: &[(f64, f64)], expected: f64, smooth: bool, failures: &mut Vec<String>) {
        let span = match (samples.first(), samples.last()) {
            (Some(first), Some(last)) => last.0 - first.0,
            _ => 0.0,
        };
        let moved = samples.last().map_or(0.0, |sample| sample.1 - START_Y);
        println!(
            "{label}: moved {moved:.1} px (expected {expected:.1}) over {} samples in {span:.0} ms",
            samples.len()
        );
        if (moved - expected).abs() > 1.0 {
            failures.push(format!("{label}: moved {moved:.1}, expected {expected:.1}"));
        }
        if smooth && (samples.len() < 4 || span < 60.0) {
            failures.push(format!("{label}: motion was not spread over frames"));
        }
    }

    fn scroll_to(probe: &Driver, y: f64) {
        probe.eval(&format!("scroller.scrollTop = {y}; 0"));
        probe.spin(Duration::from_millis(150));
        probe.eval("samples.length = 0; 0");
    }

    fn samples(probe: &Driver) -> Vec<(f64, f64)> {
        let json = probe.eval("JSON.stringify(samples)").unwrap_or_default();
        serde_json::from_str(&json).unwrap_or_default()
    }
}
