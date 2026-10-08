//! A real WKWebView in a window, driven through the application the way the
//! app is: events dispatch through `sendEvent:`, where the host's wheel
//! smoothing monitor runs, and page script is evaluated on the main thread.

use std::cell::RefCell;
use std::rc::Rc;
use std::time::{Duration, Instant};

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::AnyObject;
use objc2::{msg_send, MainThreadMarker, MainThreadOnly};
use objc2_app_kit::{
    NSApplication, NSApplicationActivationPolicy, NSBackingStoreType, NSEvent, NSEventMask, NSWindow,
    NSWindowStyleMask,
};
use objc2_core_foundation::{CFRetained, CGPoint};
use objc2_core_graphics::{CGEvent, CGEventField, CGEventType, CGMouseButton, CGScrollEventUnit};
use objc2_foundation::{NSDate, NSDefaultRunLoopMode, NSError, NSPoint, NSRect, NSSize, NSString, NSURL, NSURLRequest};
use objc2_web_kit::{
    WKUserScript, WKUserScriptInjectionTime, WKWebView, WKWebViewConfiguration, WKWebsiteDataStore,
};
use wheel_glide::glide::{GlideReport, PIXELS_PER_LINE};

pub struct Driver {
    pub app: Retained<NSApplication>,
    pub window: Retained<NSWindow>,
    pub web: Retained<WKWebView>,
    pub reports: Rc<RefCell<Vec<GlideReport>>>,
}

impl Driver {
    /// Opens a window whose content view is a web view with its own ephemeral storage, runs
    /// `document_start` before every page's own script, and installs the host's wheel smoothing.
    pub fn open(mtm: MainThreadMarker, size: NSSize, document_start: Option<&str>) -> Driver {
        let app = NSApplication::sharedApplication(mtm);
        app.setActivationPolicy(NSApplicationActivationPolicy::Regular);
        let frame = NSRect::new(NSPoint::new(200.0, 200.0), size);
        // SAFETY: a titled window over a standard content rect.
        let window = unsafe {
            NSWindow::initWithContentRect_styleMask_backing_defer(
                NSWindow::alloc(mtm),
                frame,
                NSWindowStyleMask::Titled,
                NSBackingStoreType::Buffered,
                false,
            )
        };
        // SAFETY: the window stays owned by this driver.
        unsafe { window.setReleasedWhenClosed(false) };
        let config = unsafe { WKWebViewConfiguration::new(mtm) };
        // SAFETY: configuration setters on the main thread, before the web view exists.
        unsafe {
            // Nothing a person or an earlier run stored reaches this run.
            config.setWebsiteDataStore(&WKWebsiteDataStore::nonPersistentDataStore(mtm));
            if let Some(source) = document_start {
                let script = WKUserScript::initWithSource_injectionTime_forMainFrameOnly(
                    WKUserScript::alloc(mtm),
                    &NSString::from_str(source),
                    WKUserScriptInjectionTime::AtDocumentStart,
                    true,
                );
                config.userContentController().addUserScript(&script);
            }
        }
        // SAFETY: a fresh configuration and a frame-sized web view.
        let web = unsafe {
            WKWebView::initWithFrame_configuration(
                WKWebView::alloc(mtm),
                NSRect::new(NSPoint::new(0.0, 0.0), frame.size),
                &config,
            )
        };
        window.setContentView(Some(&web));
        window.makeKeyAndOrderFront(None);
        app.activate();

        let reports = Rc::new(RefCell::new(Vec::new()));
        let sink = Rc::clone(&reports);
        wheel_glide::install(Rc::new(move |entry| sink.borrow_mut().push(entry)))
            .expect("install wheel smoothing");
        Driver { app, window, web, reports }
    }

    pub fn load_html(&self, page: &str) {
        // SAFETY: inline HTML with no base URL.
        unsafe { self.web.loadHTMLString_baseURL(&NSString::from_str(page), None) };
        self.spin(Duration::from_millis(800));
    }

    pub fn load_url(&self, url: &str) {
        let url = NSURL::URLWithString(&NSString::from_str(url)).expect("valid URL");
        // SAFETY: a plain GET request for a live URL.
        unsafe { self.web.loadRequest(&NSURLRequest::requestWithURL(&url)) };
    }

    /// Runs the event loop, dispatching through `sendEvent:` like the app.
    pub fn spin(&self, duration: Duration) {
        let deadline = Instant::now() + duration;
        while Instant::now() < deadline {
            let until = NSDate::dateWithTimeIntervalSinceNow(0.004);
            // SAFETY: the default mode on the main thread.
            let event = unsafe {
                self.app.nextEventMatchingMask_untilDate_inMode_dequeue(
                    NSEventMask::Any,
                    Some(&until),
                    NSDefaultRunLoopMode,
                    true,
                )
            };
            if let Some(event) = event {
                self.app.sendEvent(&event);
            }
        }
    }

    /// Evaluates `script` and returns the description of its value.
    pub fn eval(&self, script: &str) -> Option<String> {
        let result: Rc<RefCell<Option<Option<String>>>> = Rc::new(RefCell::new(None));
        let slot = Rc::clone(&result);
        let done = RcBlock::new(move |value: *mut AnyObject, _error: *mut NSError| {
            // SAFETY: WebKit passes a live result object or null.
            let text = unsafe { value.as_ref() }.map(|object| {
                // SAFETY: every object answers `description` with a string.
                let description: Retained<NSString> = unsafe { msg_send![object, description] };
                description.to_string()
            });
            *slot.borrow_mut() = Some(text);
        });
        // SAFETY: the completion block outlives the call.
        unsafe {
            self.web
                .evaluateJavaScript_completionHandler(&NSString::from_str(script), Some(&done))
        };
        let deadline = Instant::now() + Duration::from_secs(2);
        while result.borrow().is_none() && Instant::now() < deadline {
            self.spin(Duration::from_millis(5));
        }
        let text = result.borrow_mut().take().flatten();
        text
    }

    /// Awaits an async page expression and returns its value as JSON, or an error string.
    pub fn eval_async(&self, expression: &str, timeout: Duration) -> Result<String, String> {
        self.eval(&format!(
            "window.__driverResult = undefined; (async () => ({expression}))().then(\
             (v) => {{ window.__driverResult = 'ok:' + JSON.stringify(v ?? null); }},\
             (e) => {{ window.__driverResult = 'error:' + (e && e.message ? e.message : e); }}); 0"
        ));
        let deadline = Instant::now() + timeout;
        while Instant::now() < deadline {
            self.spin(Duration::from_millis(50));
            let Some(value) = self.eval("window.__driverResult ?? ''") else { continue };
            if let Some(json) = value.strip_prefix("ok:") {
                return Ok(json.to_string());
            }
            if let Some(error) = value.strip_prefix("error:") {
                return Err(error.to_string());
            }
        }
        Err(format!("timed out after {timeout:?}: {expression}"))
    }

    /// WebKit's scrolling tree, as its scrolling thread holds it.
    pub fn scrolling_tree(&self) -> String {
        // SAFETY: a WKWebView testing SPI that returns a string.
        let text: Option<Retained<NSString>> = unsafe { msg_send![&*self.web, _scrollingTreeAsText] };
        text.map(|text| text.to_string()).unwrap_or_default()
    }

    /// Sends one notch of `lines` (positive scrolls up) at `point` in window
    /// coordinates through the application, where local monitors run; returns
    /// the distance WebKit would scroll for it natively.
    pub fn send_notch(&self, lines: i32, point: NSPoint) -> f64 {
        let event = self.wheel_event(CGScrollEventUnit::Line, lines, true, point);
        let expected = -event.deltaY() * PIXELS_PER_LINE;
        self.app.sendEvent(&event);
        expected
    }

    /// Presses the left button at `point` in window coordinates through the
    /// application, where local monitors run, and holds it for `hold`.
    pub fn send_click(&self, point: NSPoint, hold: Duration) {
        self.send_button(CGEventType::LeftMouseDown, point);
        self.spin(hold);
        self.send_button(CGEventType::LeftMouseUp, point);
    }

    /// Presses at `from` and drags `dy` points down over `duration`, as a text selection does,
    /// then releases.
    pub fn send_drag(&self, from: NSPoint, dy: f64, duration: Duration) {
        const STEPS: u32 = 12;
        self.send_button(CGEventType::LeftMouseDown, from);
        let mut at = from;
        for step in 1..=STEPS {
            self.spin(duration / STEPS);
            // Window coordinates grow upward.
            at = NSPoint::new(from.x, from.y - dy * f64::from(step) / f64::from(STEPS));
            self.send_button(CGEventType::LeftMouseDragged, at);
        }
        self.send_button(CGEventType::LeftMouseUp, at);
    }

    fn send_button(&self, kind: CGEventType, point: NSPoint) {
        let cg = CGEvent::new_mouse_event(None, kind, CGPoint::new(0.0, 0.0), CGMouseButton::Left)
            .expect("mouse event");
        CGEvent::set_integer_value_field(Some(&cg), CGEventField::MouseEventClickState, 1);
        let event = self.aim(&cg, Some(&self.window), point);
        self.app.sendEvent(&event);
    }

    /// A wheel event aimed at `target` in window coordinates. With `windowed`,
    /// the window is attached to the synthesized event.
    pub fn wheel_event(
        &self,
        unit: CGScrollEventUnit,
        amount: i32,
        windowed: bool,
        target: NSPoint,
    ) -> Retained<NSEvent> {
        let cg = CGEvent::new_scroll_wheel_event2(None, unit, 1, amount, 0, 0).expect("scroll event");
        if unit == CGScrollEventUnit::Pixel {
            CGEvent::set_integer_value_field(Some(&cg), CGEventField::ScrollWheelEventIsContinuous, 1);
        }
        self.aim(&cg, windowed.then_some(&*self.window), target)
    }

    /// `cg` as an event at `target` in window coordinates.
    fn aim(&self, cg: &CFRetained<CGEvent>, window: Option<&NSWindow>, target: NSPoint) -> Retained<NSEvent> {
        // Measure how AppKit maps the CG location, then aim at the target.
        let origin = self.seen_at(cg, window, 0.0, 0.0);
        let unit_step = self.seen_at(cg, window, 1.0, 1.0);
        CGEvent::set_location(
            Some(cg),
            CGPoint::new(
                (target.x - origin.x) / (unit_step.x - origin.x),
                (target.y - origin.y) / (unit_step.y - origin.y),
            ),
        );
        let event = self.build(cg, window);
        assert_eq!(event.locationInWindow(), target, "event aimed off the web view");
        event
    }

    fn seen_at(&self, cg: &CFRetained<CGEvent>, window: Option<&NSWindow>, x: f64, y: f64) -> NSPoint {
        CGEvent::set_location(Some(cg), CGPoint::new(x, y));
        self.build(cg, window).locationInWindow()
    }

    fn build(&self, cg: &CGEvent, window: Option<&NSWindow>) -> Retained<NSEvent> {
        let event = NSEvent::eventWithCGEvent(cg).expect("wheel event");
        if let Some(window) = window {
            // SAFETY: Attaches the private window pointer to the event.
            unsafe {
                let _: () = msg_send![&*event, setValue: window, forKey: &*NSString::from_str("_window")];
            }
        }
        event
    }
}
