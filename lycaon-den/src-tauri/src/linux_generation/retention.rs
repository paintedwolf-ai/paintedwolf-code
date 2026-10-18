use std::io::{BufRead, BufReader, Read};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

pub(crate) struct Retention {
    child: Child,
    stdin: Option<ChildStdin>,
}

/// A dedicated package helper holds the shared Go root policy for this shell.
/// Closing stdin releases it on orderly shutdown or an unexpected shell exit.
pub(crate) fn retain() -> Result<Option<Retention>, String> {
    let Some(generation) = super::running()? else {
        return Ok(None);
    };
    let image = generation.helper("pw")?;
    let mut child = Command::new(&image.path)
        .arg("internal-nix-retain")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| format!("Start Nix generation retention: {e}"))?;
    let stdin = child.stdin.take();
    let output = child
        .stdout
        .take()
        .ok_or("Nix retention readiness pipe unavailable")?;
    let mut guard = Retention { child, stdin };
    let (sender, ready) = mpsc::channel();
    std::thread::spawn(move || {
        let mut line = String::new();
        let result = BufReader::new(output.take(64))
            .read_line(&mut line)
            .is_ok_and(|n| n > 0 && line == "ready\n");
        let _ = sender.send(result);
    });
    if ready.recv_timeout(Duration::from_secs(40)) != Ok(true)
        || guard.child.try_wait().map_err(|e| e.to_string())?.is_some()
    {
        return Err("The running Nix generation could not be retained. Check that its Nix daemon and private cache directory are available, then reopen Painted Wolf Code. Saved credentials are preserved.".into());
    }
    Ok(Some(guard))
}

impl Drop for Retention {
    fn drop(&mut self) {
        self.stdin.take();
        let deadline = Instant::now() + Duration::from_secs(2);
        while Instant::now() < deadline {
            match self.child.try_wait() {
                Ok(Some(_)) => return,
                Err(_) => break,
                Ok(None) => std::thread::sleep(Duration::from_millis(20)),
            }
        }
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}
