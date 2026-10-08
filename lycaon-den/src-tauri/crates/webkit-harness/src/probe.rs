//! Finds whether this host's WebKit scrolls on its own thread, before anything
//! is built to check that thread. A virtual machine's WebKit builds no
//! scrolling tree, so there is nothing to check there; every physical Mac
//! builds one, so a missing tree on hardware is a failure.

use std::ffi::CStr;
use std::time::{Duration, Instant};

use objc2::MainThreadMarker;
use objc2_foundation::NSSize;

use crate::driver::Driver;

/// Exit status when a virtual machine's WebKit has no scrolling thread.
pub const ABSENT_ON_VIRTUAL_MACHINE: i32 = 3;

/// A scrolling document holding an overflow scroller, the two node kinds the chat needs.
const PAGE: &str = r#"<!doctype html><body style="margin:0;height:3000px">
<div style="overflow:auto;height:200px"><div style="height:2000px"></div></div></body>"#;
const WINDOW: NSSize = NSSize::new(800.0, 600.0);
/// Longer than WebKit takes to commit its first layer tree on a loaded host.
const TREE_WAIT: Duration = Duration::from_secs(5);
const POLL: Duration = Duration::from_millis(100);

pub fn run() -> i32 {
    let mtm = MainThreadMarker::new().expect("main thread");
    let driver = Driver::open(mtm, WINDOW, None);
    driver.load_html(PAGE);
    let deadline = Instant::now() + TREE_WAIT;
    let mut threaded = !driver.scrolling_tree().trim().is_empty();
    while !threaded && Instant::now() < deadline {
        driver.spin(POLL);
        threaded = !driver.scrolling_tree().trim().is_empty();
    }
    let virtual_machine = match hypervisor_guest() {
        Ok(value) => value,
        Err(error) => {
            println!("FAIL: could not read kern.hv_vmm_present: {error}");
            return 1;
        }
    };
    println!(
        "webkit-harness probe: scrolling thread {}, virtual machine {}",
        yes_no(threaded),
        yes_no(virtual_machine)
    );
    match (threaded, virtual_machine) {
        (true, _) => 0,
        (false, true) => {
            println!("SKIP: this virtual machine's WebKit has no scrolling thread, so there is nothing to check");
            ABSENT_ON_VIRTUAL_MACHINE
        }
        (false, false) => {
            println!("FAIL: WebKit built no scrolling tree on physical hardware");
            1
        }
    }
}

fn yes_no(value: bool) -> &'static str {
    if value {
        "yes"
    } else {
        "no"
    }
}

/// The kernel's own record of running under a hypervisor.
fn hypervisor_guest() -> Result<bool, std::io::Error> {
    const NAME: &CStr = c"kern.hv_vmm_present";
    let mut value: libc::c_int = 0;
    let mut size = std::mem::size_of::<libc::c_int>();
    // SAFETY: an integer sysctl read into a buffer of its size.
    let status = unsafe {
        libc::sysctlbyname(
            NAME.as_ptr(),
            (&mut value as *mut libc::c_int).cast(),
            &mut size,
            std::ptr::null_mut(),
            0,
        )
    };
    if status != 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(value == 1)
}
