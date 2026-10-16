//! Choosing the device under `auto`.
//!
//! Every device this build can reach runs the reference batch, and the fastest one within the
//! requirement serves. Nothing about a device's kind decides: an integrated GPU that is fast
//! enough serves, and a discrete one that is not does not.
//!
//! The engine binary measures each candidate in a child process ([`EngineOptions::probe_exe`]),
//! because a faulty driver can crash its process outright; the crash then costs that candidate,
//! not the engine. The verdict persists in the cache directory, keyed by the engine build, the
//! checkpoint, the requirement and the hardware, so later launches load the chosen device
//! straight away and a machine where nothing qualifies refuses without loading anything. A
//! launch that dies while loading its device leaves a marker naming it, and the next launch
//! measures again without that device.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};

use crate::device::DeviceChoice;
use crate::engine::{Engine, EngineOptions, Prepared, millis};
use crate::error::{Error, Result};
use crate::protocol::ProbeReport;

const VERDICT_FILE: &str = "devices.json";
/// Names the device a launch is loading; removed once it serves.
const LOADING_FILE: &str = "devices.loading";
/// How long one child measurement may take: loading the checkpoint and compiling a GPU's
/// kernels for the first time.
const PROBE_TIMEOUT: Duration = Duration::from_secs(180);

/// What a verdict was measured against; any change measures again.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
struct Basis {
    build: String,
    checkpoint: String,
    qualify_ms: Option<f64>,
    hardware: Vec<String>,
}

/// One candidate's result. A candidate stopped early by a faster one or by the requirement
/// records the run that stopped it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
struct Measured {
    device: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    probe_ms: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
struct Verdict {
    basis: Basis,
    /// The device that serves, or none when no candidate qualified.
    chosen: Option<String>,
    measured: Vec<Measured>,
}

/// Load onto the fastest qualifying device.
pub(crate) fn choose(prepared: &Prepared, opts: &EngineOptions) -> Result<Engine> {
    let candidates = DeviceChoice::candidates();
    let basis = Basis {
        build: build_id(),
        checkpoint: prepared.label().to_string(),
        qualify_ms: opts.qualify.map(millis),
        hardware: candidates.iter().map(|c| c.hardware_key()).collect(),
    };
    let dir = opts.cache_dir.as_deref();
    let crashed = dir.and_then(|d| std::fs::read_to_string(d.join(LOADING_FILE)).ok());
    let previous = dir.and_then(|d| read(&d.join(VERDICT_FILE))).filter(|v| v.basis == basis);

    if let Some(device) = &crashed {
        eprintln!("bialy: the last launch did not finish loading {device}; measuring again without it");
    } else if let Some(verdict) = &previous {
        match verdict.chosen.as_deref().map(str::parse::<DeviceChoice>) {
            None => return Err(no_capable_device(&verdict.measured)),
            Some(Ok(choice)) => {
                if let Some(engine) = load_chosen(prepared, choice, opts, None) {
                    return Ok(engine);
                }
                eprintln!("bialy: {choice} no longer meets the requirement; measuring again");
            }
            Some(Err(_)) => {}
        }
    }

    let excluded = crashed.as_deref().map(str::trim);
    let mut measured = measure(prepared, &candidates, opts, excluded);
    let mut ranked: Vec<&Measured> = measured
        .iter()
        .filter(|m| m.error.is_none() && m.probe_ms.is_some_and(|ms| qualifies_ms(ms, opts.qualify)))
        .collect();
    ranked.sort_by(|a, b| a.probe_ms.partial_cmp(&b.probe_ms).unwrap_or(std::cmp::Ordering::Equal));
    let winner = ranked.first().map(|m| (m.device.clone(), m.probe_ms));
    let engine = match winner {
        Some((device, probe_ms)) => {
            let choice: DeviceChoice = device.parse()?;
            let recorded = probe_ms.map(|ms| Duration::from_secs_f64(ms / 1000.0));
            match load_chosen(prepared, choice, opts, recorded) {
                Some(engine) => Some(engine),
                None => {
                    if let Some(m) = measured.iter_mut().find(|m| m.device == device) {
                        m.error = Some("measured, but did not load in the engine".into());
                    }
                    None
                }
            }
        }
        None => None,
    };
    let verdict = Verdict {
        basis,
        chosen: engine.as_ref().map(|e| e.identity().device.clone()),
        measured,
    };
    if let Some(dir) = dir {
        match write(&dir.join(VERDICT_FILE), &verdict) {
            // The verdict now records the device that died, so the marker has served.
            Ok(()) if crashed.is_some() => {
                let _ = std::fs::remove_file(dir.join(LOADING_FILE));
            }
            Ok(()) => {}
            Err(e) => eprintln!("bialy: device verdict not saved: {e}"),
        }
    }
    engine.ok_or_else(|| no_capable_device(&verdict.measured))
}

/// Load `choice` in this process under the loading marker. Without a `measured` time the
/// device is probed again and must still qualify.
fn load_chosen(
    prepared: &Prepared,
    choice: DeviceChoice,
    opts: &EngineOptions,
    measured: Option<Duration>,
) -> Option<Engine> {
    let marker = opts.cache_dir.as_deref().map(|d| d.join(LOADING_FILE));
    if let Some(marker) = &marker
        && let Err(e) = write_atomically(marker, choice.to_string().as_bytes())
    {
        eprintln!("bialy: loading marker not written: {e}");
    }
    let engine = (|| {
        let mut engine = Engine::on(prepared, choice, opts).ok()?;
        let elapsed = match measured {
            Some(elapsed) => elapsed,
            None => engine.probe().ok().filter(|t| qualifies(*t, opts.qualify))?,
        };
        engine.record_probe(elapsed);
        Some(engine)
    })();
    if let Some(marker) = &marker {
        let _ = std::fs::remove_file(marker);
    }
    engine
}

/// Measure every candidate but `excluded`, each stopping once it is slower than the requirement
/// or the fastest so far.
fn measure(
    prepared: &Prepared,
    candidates: &[DeviceChoice],
    opts: &EngineOptions,
    excluded: Option<&str>,
) -> Vec<Measured> {
    let mut best: Option<Duration> = None;
    let mut measured = Vec::with_capacity(candidates.len());
    for &choice in candidates {
        let device = choice.to_string();
        if excluded == Some(device.as_str()) {
            measured.push(Measured {
                device,
                probe_ms: None,
                error: Some("the engine died while loading it".into()),
            });
            continue;
        }
        let ceiling = match (best, opts.qualify) {
            (Some(t), Some(limit)) => Some(t.min(limit)),
            (Some(t), None) => Some(t),
            (None, limit) => limit,
        };
        let started = Instant::now();
        let result = match &opts.probe_exe {
            Some(exe) => probe_in_child(exe, choice, opts, ceiling),
            None => Engine::on(prepared, choice, opts).and_then(|e| e.probe_within(ceiling)),
        };
        match result {
            Ok(elapsed) => {
                eprintln!(
                    "bialy: {device} reference batch {:.1} ms ({:.1} s with loading)",
                    millis(elapsed),
                    started.elapsed().as_secs_f64()
                );
                if qualifies(elapsed, opts.qualify) && best.is_none_or(|t| elapsed < t) {
                    best = Some(elapsed);
                }
                measured.push(Measured { device, probe_ms: Some(millis(elapsed)), error: None });
            }
            Err(e) => {
                eprintln!("bialy: {device} unavailable: {e}");
                measured.push(Measured { device, probe_ms: None, error: Some(e.to_string()) });
            }
        }
    }
    measured
}

/// Run `exe probe` for one candidate and read the time it reports.
fn probe_in_child(
    exe: &Path,
    choice: DeviceChoice,
    opts: &EngineOptions,
    ceiling: Option<Duration>,
) -> Result<Duration> {
    let mut command = Command::new(exe);
    command
        .arg("probe")
        .arg("--model")
        .arg(&opts.model_dir)
        .arg("--device")
        .arg(choice.to_string())
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit());
    if let Some(limit) = ceiling {
        command.arg("--qualify-ms").arg(limit.as_millis().max(1).to_string());
    }
    if !opts.dtype.is_empty() {
        command.arg("--dtype").arg(&opts.dtype);
    }
    if let Some(dir) = &opts.cache_dir {
        command.arg("--cache-dir").arg(dir);
    }
    if let Some(path) = &opts.metallib {
        command.arg("--metallib").arg(path);
    }
    let fail = |why: String| Error::Device(format!("{choice} probe {why}"));
    let mut child = command.spawn().map_err(|e| fail(format!("did not start: {e}")))?;
    let started = Instant::now();
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if started.elapsed() > PROBE_TIMEOUT => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(fail(format!("took longer than {} s", PROBE_TIMEOUT.as_secs())));
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(50)),
            Err(e) => return Err(fail(format!("could not be waited on: {e}"))),
        }
    };
    let mut stdout = String::new();
    if let Some(mut out) = child.stdout.take() {
        use std::io::Read;
        let _ = out.read_to_string(&mut stdout);
    }
    if !status.success() {
        return Err(fail(format!("exited with {status}")));
    }
    let report: ProbeReport = serde_json::from_str(stdout.trim())
        .map_err(|e| fail(format!("reported {:?}: {e}", stdout.trim())))?;
    Ok(Duration::from_secs_f64(report.probe_ms / 1000.0))
}

fn qualifies(elapsed: Duration, requirement: Option<Duration>) -> bool {
    requirement.is_none_or(|limit| elapsed <= limit)
}

fn qualifies_ms(ms: f64, requirement: Option<Duration>) -> bool {
    requirement.is_none_or(|limit| ms <= millis(limit))
}

fn no_capable_device(measured: &[Measured]) -> Error {
    let summary = measured
        .iter()
        .map(|m| match (m.probe_ms, &m.error) {
            (Some(ms), _) => format!("{} {ms:.1} ms", m.device),
            (None, Some(e)) => format!("{} failed ({e})", m.device),
            (None, None) => m.device.clone(),
        })
        .collect::<Vec<_>>()
        .join(", ");
    Error::NoCapableDevice(summary)
}

/// The running executable's size and modification time: a rebuilt or updated engine measures
/// again.
fn build_id() -> String {
    std::env::current_exe()
        .and_then(std::fs::metadata)
        .map(|meta| {
            let modified = meta
                .modified()
                .ok()
                .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                .map_or(0, |d| d.as_secs());
            format!("{}:{modified}", meta.len())
        })
        .unwrap_or_default()
}

fn read(path: &Path) -> Option<Verdict> {
    serde_json::from_slice(&std::fs::read(path).ok()?).ok()
}

fn write(path: &Path, verdict: &Verdict) -> std::io::Result<()> {
    write_atomically(path, &serde_json::to_vec_pretty(verdict)?)
}

/// Write through a sibling file so a reader never sees half of it.
fn write_atomically(path: &Path, data: &[u8]) -> std::io::Result<()> {
    let dir = path.parent().map(Path::to_path_buf).unwrap_or_else(|| PathBuf::from("."));
    std::fs::create_dir_all(&dir)?;
    let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("file");
    let staged = dir.join(format!(".{name}.{}", std::process::id()));
    std::fs::write(&staged, data)?;
    std::fs::rename(&staged, path)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::{scratch_dir, write_tiny_checkpoint};

    fn options(dir: &Path, cache: &Path, qualify: Option<Duration>) -> EngineOptions {
        EngineOptions {
            model_dir: dir.to_path_buf(),
            device: DeviceChoice::Auto,
            qualify,
            cache_dir: Some(cache.to_path_buf()),
            ..Default::default()
        }
    }

    /// A stand-in for the engine binary's `probe` command, run from a shell script.
    fn fake_probe(dir: &Path, body: &str) -> PathBuf {
        use std::os::unix::fs::PermissionsExt;
        let path = dir.join("fake-bialy");
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        path
    }

    #[test]
    fn auto_serves_a_qualifying_device_and_remembers_it() {
        let dir = scratch_dir("select-ok");
        write_tiny_checkpoint(&dir);
        let cache = dir.join("cache");
        let opts = options(&dir, &cache, Some(Duration::from_secs(60)));
        let engine = Engine::load(&opts).unwrap();
        assert!(engine.identity().probe_ms.is_some());
        let verdict = read(&cache.join(VERDICT_FILE)).unwrap();
        assert_eq!(verdict.chosen.as_deref(), Some(engine.identity().device.as_str()));
        assert_eq!(verdict.measured.len(), DeviceChoice::candidates().len());
        assert!(!cache.join(LOADING_FILE).exists());

        // A later launch takes the remembered device without measuring the others.
        let again = Engine::load(&opts).unwrap();
        assert_eq!(again.identity().device, engine.identity().device);
    }

    #[test]
    fn nothing_qualifying_is_refused_and_remembered() {
        let dir = scratch_dir("select-none");
        write_tiny_checkpoint(&dir);
        let cache = dir.join("cache");
        let opts = options(&dir, &cache, Some(Duration::ZERO));
        let err = Engine::load(&opts).unwrap_err();
        assert!(matches!(err, Error::NoCapableDevice(ref m) if m.contains("cpu")), "{err}");
        let verdict = read(&cache.join(VERDICT_FILE)).unwrap();
        assert!(verdict.chosen.is_none());

        // The remembered refusal holds until something it was measured against changes.
        assert!(matches!(Engine::load(&opts).unwrap_err(), Error::NoCapableDevice(_)));
        let relaxed = options(&dir, &cache, Some(Duration::from_secs(60)));
        assert!(Engine::load(&relaxed).is_ok());
    }

    #[test]
    fn without_a_requirement_the_fastest_device_serves() {
        let dir = scratch_dir("select-any");
        write_tiny_checkpoint(&dir);
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            device: DeviceChoice::Auto,
            ..Default::default()
        };
        assert!(Engine::load(&opts).unwrap().identity().probe_ms.is_some());
    }

    #[test]
    fn a_child_measurement_serves_the_device_it_reports() {
        let dir = scratch_dir("select-child");
        write_tiny_checkpoint(&dir);
        let cache = dir.join("cache");
        let mut opts = options(&dir, &cache, Some(Duration::from_secs(60)));
        opts.probe_exe = Some(fake_probe(&dir, r#"echo '{"probe_ms": 4.2}'"#));
        let engine = Engine::load(&opts).unwrap();
        assert_eq!(engine.identity().probe_ms, Some(4.2));
    }

    #[test]
    fn a_crashing_child_costs_its_candidate_not_the_engine() {
        let dir = scratch_dir("select-crash");
        write_tiny_checkpoint(&dir);
        let cache = dir.join("cache");
        let mut opts = options(&dir, &cache, Some(Duration::from_secs(60)));
        opts.probe_exe = Some(fake_probe(&dir, "kill -SEGV $$"));
        let err = Engine::load(&opts).unwrap_err();
        assert!(matches!(err, Error::NoCapableDevice(ref m) if m.contains("probe exited")), "{err}");
        // A fresh cache, or the remembered refusal would answer.
        opts.cache_dir = Some(dir.join("cache-garbled"));
        opts.probe_exe = Some(fake_probe(&dir, "echo not json"));
        let err = Engine::load(&opts).unwrap_err();
        assert!(matches!(err, Error::NoCapableDevice(ref m) if m.contains("reported")), "{err}");
    }

    #[test]
    fn a_launch_that_died_loading_its_device_measures_again_without_it() {
        let dir = scratch_dir("select-died");
        write_tiny_checkpoint(&dir);
        let cache = dir.join("cache");
        let opts = options(&dir, &cache, Some(Duration::from_secs(60)));
        let first = Engine::load(&opts).unwrap();
        let device = first.identity().device.clone();
        drop(first);
        std::fs::write(cache.join(LOADING_FILE), &device).unwrap();

        let next = Engine::load(&opts);
        let verdict = read(&cache.join(VERDICT_FILE)).unwrap();
        let record = verdict.measured.iter().find(|m| m.device == device).unwrap();
        assert!(record.error.as_deref().is_some_and(|e| e.contains("died")), "{record:?}");
        assert_ne!(verdict.chosen.as_deref(), Some(device.as_str()));
        assert_eq!(next.is_ok(), verdict.chosen.is_some());
        assert!(!cache.join(LOADING_FILE).exists());
    }

    #[test]
    fn the_refusal_names_every_candidate() {
        let err = no_capable_device(&[
            Measured { device: "vulkan:0".into(), probe_ms: Some(812.0), error: None },
            Measured { device: "cpu".into(), probe_ms: None, error: Some("boom".into()) },
        ]);
        assert_eq!(err.to_string(), "no device meets the speed requirement: vulkan:0 812.0 ms, cpu failed (boom)");
    }
}
