//! Choosing the device under `auto`.
//!
//! Every device this build can reach runs the reference batch, and the fastest one within the
//! requirement serves. Nothing about a device's kind decides: an integrated GPU that is fast
//! enough serves, and a discrete one that is not does not. The verdict persists in the cache
//! directory, keyed by the engine build, the checkpoint, the requirement and the hardware, so
//! later launches load the chosen device straight away and a machine where nothing qualifies
//! refuses without loading anything.

use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};

use crate::device::DeviceChoice;
use crate::engine::{Engine, EngineOptions, Prepared, millis};
use crate::error::{Error, Result};

const VERDICT_FILE: &str = "devices.json";

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
    let path = opts.cache_dir.as_deref().map(|dir| dir.join(VERDICT_FILE));
    if let Some(verdict) = path.as_deref().and_then(read).filter(|v| v.basis == basis) {
        match verdict.chosen.as_deref().map(str::parse::<DeviceChoice>) {
            None => return Err(no_capable_device(&verdict.measured)),
            Some(Ok(choice)) => {
                if let Some(engine) = confirm(prepared, choice, opts) {
                    return Ok(engine);
                }
                eprintln!("bialy: {choice} no longer meets the requirement; measuring again");
            }
            Some(Err(_)) => {}
        }
    }
    let (engine, measured) = measure(prepared, &candidates, opts);
    let verdict = Verdict {
        basis,
        chosen: engine.as_ref().map(|e| e.identity().device.clone()),
        measured,
    };
    if let Some(path) = &path
        && let Err(e) = write(path, &verdict)
    {
        eprintln!("bialy: device verdict not saved: {e}");
    }
    engine.ok_or_else(|| no_capable_device(&verdict.measured))
}

/// Reload the cached choice and check it still qualifies.
fn confirm(prepared: &Prepared, choice: DeviceChoice, opts: &EngineOptions) -> Option<Engine> {
    let mut engine = Engine::on(prepared, choice, opts).ok()?;
    let elapsed = engine.probe().ok()?;
    if !qualifies(elapsed, opts.qualify) {
        return None;
    }
    engine.record_probe(elapsed);
    Some(engine)
}

/// Run the reference batch on every candidate, keeping only the fastest qualifying engine.
fn measure(
    prepared: &Prepared,
    candidates: &[DeviceChoice],
    opts: &EngineOptions,
) -> (Option<Engine>, Vec<Measured>) {
    let mut best: Option<(Duration, Engine)> = None;
    let mut measured = Vec::with_capacity(candidates.len());
    for &choice in candidates {
        let ceiling = match (&best, opts.qualify) {
            (Some((t, _)), Some(limit)) => Some((*t).min(limit)),
            (Some((t, _)), None) => Some(*t),
            (None, limit) => limit,
        };
        let started = Instant::now();
        let result = Engine::on(prepared, choice, opts).and_then(|engine| {
            let loaded = started.elapsed();
            engine.probe_within(ceiling).map(|elapsed| (loaded, elapsed, engine))
        });
        match result {
            Ok((loaded, elapsed, mut engine)) => {
                let id = engine.identity();
                eprintln!(
                    "bialy: {} {} loaded in {:.1} s, reference batch {:.1} ms",
                    id.device,
                    id.adapter,
                    loaded.as_secs_f64(),
                    millis(elapsed)
                );
                measured.push(Measured {
                    device: id.device.clone(),
                    probe_ms: Some(millis(elapsed)),
                    error: None,
                });
                let faster = best.as_ref().is_none_or(|(t, _)| elapsed < *t);
                if qualifies(elapsed, opts.qualify) && faster {
                    engine.record_probe(elapsed);
                    best = Some((elapsed, engine));
                }
            }
            Err(e) => {
                eprintln!("bialy: {choice} unavailable: {e}");
                measured.push(Measured {
                    device: choice.to_string(),
                    probe_ms: None,
                    error: Some(e.to_string()),
                });
            }
        }
    }
    (best.map(|(_, engine)| engine), measured)
}

fn qualifies(elapsed: Duration, requirement: Option<Duration>) -> bool {
    requirement.is_none_or(|limit| elapsed <= limit)
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

/// Write through a sibling file so a reader never sees half a verdict.
fn write(path: &Path, verdict: &Verdict) -> std::io::Result<()> {
    let dir = path.parent().map(Path::to_path_buf).unwrap_or_else(|| PathBuf::from("."));
    std::fs::create_dir_all(&dir)?;
    let staged = dir.join(format!(".{VERDICT_FILE}.{}", std::process::id()));
    std::fs::write(&staged, serde_json::to_vec_pretty(verdict)?)?;
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
    fn the_refusal_names_every_candidate() {
        let err = no_capable_device(&[
            Measured { device: "vulkan:0".into(), probe_ms: Some(812.0), error: None },
            Measured { device: "cpu".into(), probe_ms: None, error: Some("boom".into()) },
        ]);
        assert_eq!(err.to_string(), "no device meets the speed requirement: vulkan:0 812.0 ms, cpu failed (boom)");
    }
}
