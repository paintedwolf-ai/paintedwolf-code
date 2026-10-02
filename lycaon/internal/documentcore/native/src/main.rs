//! Serves the host's text CRDT over length-prefixed frames on stdin and stdout.
//!
//! Each frame is a little-endian u32 length followed by that many bytes of JSON.
//! The process announces its protocol once, then answers one request per frame
//! in order. End of input is a clean shutdown; anything else malformed exits.

use std::alloc::{GlobalAlloc, Layout, System};
use std::io::{self, BufReader, BufWriter, ErrorKind, Read, Write};
use std::process::ExitCode;
use std::sync::atomic::{AtomicUsize, Ordering};

use document_core::{respond, MAX_FRAME_BYTES};

/// Bumped with any change to framing or to the request and response shapes.
const PROTOCOL: u32 = 1;

/// Exit status for a protocol violation by the host.
const EXIT_PROTOCOL: u8 = 2;
/// Exit status for unusable arguments.
const EXIT_USAGE: u8 = 64;

/// Refuses allocations past a ceiling, so a hostile document ends this
/// process instead of exhausting the machine. The ceiling is set once at
/// startup; until then allocation is unbounded.
struct Capped {
    used: AtomicUsize,
    limit: AtomicUsize,
}

impl Capped {
    fn admit(&self, size: usize) -> bool {
        let limit = self.limit.load(Ordering::Relaxed);
        let used = self.used.fetch_add(size, Ordering::Relaxed);
        if used.saturating_add(size) > limit {
            self.used.fetch_sub(size, Ordering::Relaxed);
            return false;
        }
        true
    }
}

unsafe impl GlobalAlloc for Capped {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        if !self.admit(layout.size()) {
            return std::ptr::null_mut();
        }
        let pointer = System.alloc(layout);
        if pointer.is_null() {
            self.used.fetch_sub(layout.size(), Ordering::Relaxed);
        }
        pointer
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        if !self.admit(layout.size()) {
            return std::ptr::null_mut();
        }
        let pointer = System.alloc_zeroed(layout);
        if pointer.is_null() {
            self.used.fetch_sub(layout.size(), Ordering::Relaxed);
        }
        pointer
    }

    unsafe fn dealloc(&self, pointer: *mut u8, layout: Layout) {
        System.dealloc(pointer, layout);
        self.used.fetch_sub(layout.size(), Ordering::Relaxed);
    }

    unsafe fn realloc(&self, pointer: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        let grown = size.saturating_sub(layout.size());
        if grown > 0 && !self.admit(grown) {
            return std::ptr::null_mut();
        }
        let moved = System.realloc(pointer, layout, size);
        if moved.is_null() {
            self.used.fetch_sub(grown, Ordering::Relaxed);
        } else if size < layout.size() {
            self.used.fetch_sub(layout.size() - size, Ordering::Relaxed);
        }
        moved
    }
}

#[global_allocator]
static ALLOCATOR: Capped = Capped { used: AtomicUsize::new(0), limit: AtomicUsize::new(usize::MAX) };

fn main() -> ExitCode {
    let limit = match memory_limit(std::env::args().skip(1)) {
        Ok(limit) => limit,
        Err(message) => {
            eprintln!("pw-document-core: {message}");
            eprintln!("usage: pw-document-core serve --memory-limit-bytes <bytes>");
            return ExitCode::from(EXIT_USAGE);
        }
    };
    ALLOCATOR.limit.store(limit, Ordering::Relaxed);
    match serve(io::stdin().lock(), io::stdout().lock()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("pw-document-core: {error}");
            ExitCode::from(EXIT_PROTOCOL)
        }
    }
}

fn memory_limit(mut args: impl Iterator<Item = String>) -> Result<usize, String> {
    if args.next().as_deref() != Some("serve") {
        return Err("expected the serve command".into());
    }
    let (Some(flag), Some(value), None) = (args.next(), args.next(), args.next()) else {
        return Err("expected exactly --memory-limit-bytes <bytes>".into());
    };
    if flag != "--memory-limit-bytes" {
        return Err(format!("unknown argument {flag}"));
    }
    match value.parse::<usize>() {
        Ok(limit) if limit > 0 => Ok(limit),
        _ => Err(format!("invalid memory limit {value}")),
    }
}

fn serve(input: impl Read, output: impl Write) -> io::Result<()> {
    let mut input = BufReader::new(input);
    let mut output = BufWriter::new(output);
    write_frame(&mut output, format!("{{\"protocol\":{PROTOCOL}}}").as_bytes())?;
    while let Some(request) = read_frame(&mut input)? {
        let response = respond(&request);
        if response.len() > MAX_FRAME_BYTES {
            write_frame(&mut output, br#"{"error":"response_too_large"}"#)?;
        } else {
            write_frame(&mut output, &response)?;
        }
    }
    Ok(())
}

/// Reads one frame; `None` is end of input on a frame boundary.
fn read_frame(input: &mut impl Read) -> io::Result<Option<Vec<u8>>> {
    let mut header = [0u8; 4];
    let mut filled = 0;
    while filled < header.len() {
        match input.read(&mut header[filled..]) {
            Ok(0) if filled == 0 => return Ok(None),
            Ok(0) => return Err(io::Error::new(ErrorKind::UnexpectedEof, "input ended inside a frame header")),
            Ok(read) => filled += read,
            Err(error) if error.kind() == ErrorKind::Interrupted => {}
            Err(error) => return Err(error),
        }
    }
    let length = u32::from_le_bytes(header) as usize;
    if length > MAX_FRAME_BYTES {
        return Err(io::Error::new(ErrorKind::InvalidData, format!("request frame of {length} bytes exceeds the limit")));
    }
    let mut frame = vec![0u8; length];
    input.read_exact(&mut frame)?;
    Ok(Some(frame))
}

fn write_frame(output: &mut impl Write, frame: &[u8]) -> io::Result<()> {
    output.write_all(&(frame.len() as u32).to_le_bytes())?;
    output.write_all(frame)?;
    output.flush()
}
