//! Holds Den's chat to the invariant WebKit's threaded scrolling makes easy to
//! break: after every native input stream settles, the offset the scrolling
//! thread paints is the offset the main thread hit tests, and both hold the
//! same content extent. When they part, every click lands a fixed distance
//! from the pointer until the next scroll.
//!
//! Drives the real harness Den in a real WKWebView, with notches replayed by
//! the host's own wheel smoothing, and reads WebKit's scrolling tree.
//!
//! Run through `./task den:webkit:scroll`, which serves the harness stack.

#[cfg(target_os = "macos")]
fn main() {
    let url = std::env::args().nth(1).expect("usage: scroll_thread_invariants <harness URL>");
    std::process::exit(macos::run(&url));
}

#[cfg(not(target_os = "macos"))]
fn main() {
    eprintln!("scroll_thread_invariants runs on macOS only");
}

#[cfg(target_os = "macos")]
#[path = "support/web_view_driver.rs"]
mod web_view_driver;

#[cfg(target_os = "macos")]
mod macos {
    use std::time::Duration;

    use objc2::MainThreadMarker;
    use objc2_foundation::{NSPoint, NSSize};
    use serde::Deserialize;

    use crate::web_view_driver::Driver;

    const PAGE: &str = include_str!("support/scroll_thread_page.js");
    /// A person who finished first-run setup and turned tips off, so nothing covers the chat.
    const RETURNING_PERSON: &str = r#"try {
  localStorage.setItem("paintedwolf.app-state.v1.onboarding", JSON.stringify({ firstRunSetupCompleted: true }));
  localStorage.setItem("paintedwolf.app-state.v1.firstTimeTips", JSON.stringify({ enabled: false }));
} catch {}"#;
    const WINDOW: NSSize = NSSize::new(1100.0, 720.0);
    /// Notches simulating a long wheel flick.
    const FLICK_NOTCHES: usize = 19;
    const NOTCH_GAP: Duration = Duration::from_millis(60);
    /// Longer than the input settle window, any reclaim, and the glide that returns held range.
    const SETTLE: Duration = Duration::from_millis(1600);
    const ROUNDS: usize = 6;
    /// How far a row above the reader settles from the height it mounted at.
    const SETTLE_ABOVE_PX: i32 = 120;
    /// When a press lands after a flick's last notch: during its glide, at its landing, and after.
    const CLICK_DELAYS_MS: [u64; 6] = [0, 40, 80, 120, 160, 240];
    /// A press held past the 150 ms of quiet after which the page lands what waited for a stream.
    const PRESS_HOLD: Duration = Duration::from_millis(550);
    /// Lines per notch in the long chat, as a mouse with a coarser wheel sends.
    const LONG_FLICK_LINES: i32 = 5;
    /// How far a selection drag moves down the chat.
    const SELECTION_DRAG_PX: f64 = 60.0;

    #[derive(Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct Geometry {
        top: f64,
        height: f64,
        client: f64,
        width: f64,
        left: f64,
        rect_top: f64,
        rect_width: f64,
        rect_height: f64,
    }

    /// One overflow node of WebKit's scrolling tree.
    #[derive(Debug, Default)]
    struct ScrollingNode {
        area: (f64, f64),
        content_height: f64,
        position_y: f64,
    }

    pub fn run(url: &str) -> i32 {
        let mtm = MainThreadMarker::new().expect("main thread");
        let den = Driver::open(mtm, WINDOW, Some(RETURNING_PERSON));
        den.load_url(url);
        if let Err(error) = boot(&den) {
            println!("FAIL: harness chat did not load: {error}");
            return 1;
        }
        // Without threaded scrolling, as on virtualized CI hosts, WebKit builds no scrolling tree.
        if den.scrolling_tree().trim().is_empty() {
            println!("SKIP: WebKit built no scrolling tree on this host, so there is no scrolling thread to check");
            return 0;
        }
        let mut failures = Vec::new();
        den.eval("__scrollThread.journal(), 0");
        check_in_sync(&den, "seeded chat at the tail", &mut failures);

        match den.eval_async("__scrollThread.updateCollapsedActivity()", Duration::from_secs(15)) {
            Ok(result) if result == "[]" => {}
            Ok(result) => failures.push(format!("collapsed activity updates moved the tail: {result}")),
            Err(error) => failures.push(format!("collapsed activity updates: {error}")),
        }
        check_in_sync(&den, "collapsed activity updates at the tail", &mut failures);

        for round in 0..ROUNDS {
            flick(&den, 8, 1);
            check_in_sync(&den, &format!("steady tail {round}: glide up"), &mut failures);
            flick(&den, FLICK_NOTCHES, -1);
            check_in_sync(&den, &format!("steady tail {round}: glide into the tail"), &mut failures);
        }

        den.eval("__scrollThread.flapTail(true)");
        for round in 0..ROUNDS {
            flick(&den, 8, 1);
            check_in_sync(&den, &format!("re-measuring tail {round}: glide up"), &mut failures);
            flick(&den, FLICK_NOTCHES, -1);
            check_in_sync(&den, &format!("re-measuring tail {round}: glide into the tail"), &mut failures);
        }
        den.eval("__scrollThread.flapTail(false)");

        for round in 0..ROUNDS {
            flick(&den, 8, 1);
            check_in_sync(&den, &format!("streaming tail {round}: glide up"), &mut failures);
            // The reply streams in while the glide lands on it and the chat resumes following.
            den.eval("__scrollThread.streamTail(1500)");
            flick(&den, FLICK_NOTCHES, -1);
            check_in_sync(&den, &format!("streaming tail {round}: glide into the tail"), &mut failures);
        }

        for (name, delta) in [("shorter", -SETTLE_ABOVE_PX), ("taller", SETTLE_ABOVE_PX)] {
            for round in 0..ROUNDS {
                // Armed at rest, so only the settles under the glide move the reader.
                den.eval(&format!("__scrollThread.settleAbove({delta})"));
                check_in_sync(&den, &format!("rows above settling {name} {round}: armed"), &mut failures);
                flick(&den, 12, 1);
                check_in_sync(&den, &format!("rows above settling {name} {round}: glide up"), &mut failures);
                den.eval("__scrollThread.settleAbove(0)");
                flick(&den, FLICK_NOTCHES, -1);
                check_in_sync(&den, &format!("rows above settling {name} {round}: glide into the tail"), &mut failures);
            }
        }

        // A press stops the host's glide where it is, anywhere in its landing; a held press is
        // still down when the page lands what waited for the stream.
        for (hold, name) in [(Duration::ZERO, "click"), (PRESS_HOLD, "held press")] {
            for (round, after) in CLICK_DELAYS_MS.into_iter().enumerate() {
                // Aimed before the flick, so reading geometry never delays the press.
                let Some(center) = chat_center(&den) else { break };
                flick(&den, 12, 1);
                den.spin(Duration::from_millis(after));
                den.send_click(center, hold);
                check_in_sync(&den, &format!("{name} into a glide {round}: up, {after} ms after the last notch"), &mut failures);
                flick(&den, FLICK_NOTCHES, -1);
                den.spin(Duration::from_millis(after));
                den.send_click(center, hold);
                check_in_sync(&den, &format!("{name} into a glide {round}: into the tail, {after} ms after the last notch"), &mut failures);
            }
        }

        for round in 0..ROUNDS / 2 {
            flick(&den, FLICK_NOTCHES, -1);
            let pressed = den.eval("__scrollThread.pressNewestDisclosure()").unwrap_or_default();
            check_in_sync(&den, &format!("disclosure at the tail {round}: open {pressed}"), &mut failures);
            flick(&den, FLICK_NOTCHES, -1);
            let pressed = den.eval("__scrollThread.pressNewestDisclosure()").unwrap_or_default();
            check_in_sync(&den, &format!("disclosure at the tail {round}: close {pressed}"), &mut failures);
        }

        // A chat long enough that rows unmount far above and mount at their estimates on the way
        // back; flicks of several lines, a selection dragged as the last glide lands.
        match den.eval_async("__scrollThread.extend(36)", Duration::from_secs(900)) {
            Ok(height) => println!("long chat: {height} px"),
            Err(error) => failures.push(format!("long chat did not load: {error}")),
        }
        for (round, after) in CLICK_DELAYS_MS.into_iter().enumerate() {
            let Some(center) = chat_center(&den) else { break };
            flick(&den, 10, LONG_FLICK_LINES);
            den.spin(Duration::from_millis(after));
            den.send_drag(center, SELECTION_DRAG_PX, PRESS_HOLD);
            check_in_sync(&den, &format!("long chat {round}: selection {after} ms after a glide up"), &mut failures);
            for _ in 0..3 {
                flick(&den, 5, -LONG_FLICK_LINES);
                den.spin(Duration::from_millis(300));
            }
            flick(&den, 5, -LONG_FLICK_LINES);
            den.spin(Duration::from_millis(after));
            den.send_drag(center, SELECTION_DRAG_PX, PRESS_HOLD);
            check_in_sync(&den, &format!("long chat {round}: selection {after} ms after flicks into the tail"), &mut failures);
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

    fn boot(den: &Driver) -> Result<(), String> {
        let mut attempts = Vec::new();
        for _ in 0..3 {
            den.spin(Duration::from_secs(2));
            den.eval(PAGE);
            let outcome = match den.eval_async("__scrollThread.seed()", Duration::from_secs(120)) {
                Ok(rows) if rows.parse::<u32>().unwrap_or(0) >= 3 => return Ok(()),
                Ok(rows) => format!("{rows} transcript rows"),
                Err(error) => error,
            };
            let progress = den.eval("String(__scrollThread.progress)").unwrap_or_default();
            let state = den
                .eval("window.__harness ? JSON.stringify(__harness.state()) : 'no harness'")
                .unwrap_or_default();
            attempts.push(format!("{outcome} (last step: {progress}; state: {state})"));
            // SAFETY: reloads the page this driver loaded.
            let _ = unsafe { den.web.reload() };
        }
        Err(attempts.join("\n  "))
    }

    fn geometry(den: &Driver) -> Option<Geometry> {
        let json = den.eval("JSON.stringify(__scrollThread.geometry())")?;
        serde_json::from_str(&json).ok()
    }

    /// Notches over the middle of the chat, spaced like a flick; `lines` is
    /// positive to scroll up.
    fn flick(den: &Driver, notches: usize, lines: i32) {
        let Some(point) = chat_center(den) else { return };
        for _ in 0..notches {
            den.send_notch(lines, point);
            den.spin(NOTCH_GAP);
        }
    }

    /// The middle of the chat in the window content view's coordinates, whose origin is its bottom left.
    fn chat_center(den: &Driver) -> Option<NSPoint> {
        let g = geometry(den)?;
        Some(NSPoint::new(g.left + g.rect_width / 2.0, WINDOW.height - (g.rect_top + g.rect_height / 2.0)))
    }

    fn check_in_sync(den: &Driver, label: &str, failures: &mut Vec<String>) {
        den.spin(SETTLE);
        let Some(g) = geometry(den) else {
            failures.push(format!("{label}: chat geometry unreadable"));
            return;
        };
        let tree = den.scrolling_tree();
        let journal = den.eval("__scrollThread.journal()").unwrap_or_default();
        let failed_before = failures.len();
        let Some(node) = chat_node(&tree, &g) else {
            failures.push(format!("{label}: no scrolling node for the chat ({}x{})\n{tree}", g.width, g.client));
            return;
        };
        println!(
            "{label}: scrolling thread at {:.0} of {:.0}, main thread at {:.0} of {:.0}",
            node.position_y, node.content_height, g.top, g.height
        );
        if (node.position_y - g.top).abs() > 1.0 {
            failures.push(format!(
                "{label}: painted at offset {:.0} but hit testing at {:.0}; clicks land {:.0}px from the pointer",
                node.position_y,
                g.top,
                node.position_y - g.top
            ));
        }
        if (node.content_height - g.height).abs() > 1.0 {
            failures.push(format!(
                "{label}: scrolling thread holds extent {:.0}, main thread {:.0}",
                node.content_height, g.height
            ));
        }
        if failures.len() > failed_before {
            println!("  main thread since the previous check:\n    {}", journal.replace('\n', "\n    "));
        }
    }

    /// The overflow node whose scrollable area is the chat viewport.
    fn chat_node(tree: &str, g: &Geometry) -> Option<ScrollingNode> {
        parse_overflow_nodes(tree)
            .into_iter()
            .filter(|node| (node.area.0 - g.width).abs() <= 1.0 && (node.area.1 - g.client).abs() <= 1.0)
            .min_by(|a, b| {
                let da = (a.content_height - g.height).abs();
                let db = (b.content_height - g.height).abs();
                da.total_cmp(&db)
            })
    }

    fn parse_overflow_nodes(tree: &str) -> Vec<ScrollingNode> {
        let mut nodes = Vec::new();
        let mut current: Option<ScrollingNode> = None;
        for line in tree.lines().map(str::trim) {
            if line.starts_with("(overflow scrolling node") {
                nodes.extend(current.take());
                current = Some(ScrollingNode::default());
                continue;
            }
            let Some(node) = current.as_mut() else { continue };
            if let Some(rest) = line.strip_prefix("(scrollable area size ") {
                node.area = size(rest);
            } else if let Some(rest) = line.strip_prefix("(total content size ") {
                node.content_height = size(rest).1;
            } else if let Some(rest) = line.strip_prefix("(scroll position (") {
                node.position_y = rest
                    .split(',')
                    .nth(1)
                    .and_then(|y| y.trim_end_matches(')').trim().parse().ok())
                    .unwrap_or(f64::NAN);
            }
        }
        nodes.extend(current);
        nodes
    }

    /// Reads `width=W height=H)`.
    fn size(text: &str) -> (f64, f64) {
        let field = |name: &str| {
            text.split_whitespace()
                .find_map(|part| part.strip_prefix(name))
                .and_then(|value| value.trim_end_matches(')').parse().ok())
                .unwrap_or(f64::NAN)
        };
        (field("width="), field("height="))
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        const TREE: &str = "(scrolling tree
  (frame scrolling node
    (scrollable area size width=1100 height=720)
    (total content size width=1100 height=720)
    (overflow scrolling node
      (scrollable area size width=786 height=600)
      (total content size width=786 height=2387)
      (last committed scroll position (0,1787))
      (scroll position (0,1832)))
    (overflow scrolling node
      (scrollable area size width=240 height=600)
      (total content size width=240 height=900)
      (scroll position (0,0)))))";

        #[test]
        fn reads_each_overflow_node() {
            let nodes = parse_overflow_nodes(TREE);
            assert_eq!(nodes.len(), 2);
            assert_eq!(nodes[0].area, (786.0, 600.0));
            assert_eq!(nodes[0].content_height, 2387.0);
            assert_eq!(nodes[0].position_y, 1832.0);
        }

        #[test]
        fn picks_the_chat_by_its_viewport() {
            let g = Geometry {
                top: 1787.0,
                height: 2387.0,
                client: 600.0,
                width: 786.0,
                left: 304.0,
                rect_top: 36.0,
                rect_width: 786.0,
                rect_height: 599.5,
            };
            let node = chat_node(TREE, &g).expect("chat node");
            assert_eq!(node.position_y, 1832.0);
        }
    }
}
