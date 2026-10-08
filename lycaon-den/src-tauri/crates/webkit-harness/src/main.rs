//! Drives Den's chat in a real WKWebView, through the host's own wheel glide,
//! to check what only WebKit's threaded scrolling can break.
//!
//! - `probe`: whether this host's WebKit scrolls on its own thread
//! - `scroll-invariants <harness URL>`: the scrolling thread and the main thread agree
//! - `wheel-glide`: notches glide the whole distance on every attached display
//!
//! `./task den:webkit:scroll` runs the probe, then the invariants against the harness stack.

#[cfg(target_os = "macos")]
mod driver;
#[cfg(target_os = "macos")]
mod probe;
#[cfg(target_os = "macos")]
mod scroll_invariants;
#[cfg(target_os = "macos")]
mod wheel_glide;

const USAGE: &str = "usage: webkit-harness probe | scroll-invariants <harness URL> | wheel-glide";

#[cfg(target_os = "macos")]
fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let status = match args.iter().map(String::as_str).collect::<Vec<_>>().as_slice() {
        ["probe"] => probe::run(),
        ["scroll-invariants", url] => scroll_invariants::run(url),
        ["wheel-glide"] => wheel_glide::run(),
        _ => {
            eprintln!("{USAGE}");
            2
        }
    };
    std::process::exit(status);
}

#[cfg(not(target_os = "macos"))]
fn main() {
    eprintln!("webkit-harness drives macOS WebKit; nothing runs here ({USAGE})");
}
