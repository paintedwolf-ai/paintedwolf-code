//! `bialy`: serve Bialy to the host over stdio.

use std::path::PathBuf;
use std::process::ExitCode;
use std::time::{Duration, Instant};

use bialy::engine::{HEAD_CODE_RANK, HEAD_NAMES, REFERENCE_TASK, reference_candidates};
use bialy::protocol::{EXIT_NO_CAPABLE_DEVICE, ProbeReport};
use bialy::{DeviceChoice, Engine, EngineOptions, Error};
use clap::{Parser, Subcommand};

#[derive(Parser)]
#[command(name = "bialy", version, about = "Painted Wolf Code decision engine")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(clap::Args)]
struct LoadArgs {
    /// The checkpoint directory (rl_agent_config.json, model.safetensors, encoder/, tokenizer/).
    #[arg(long, value_name = "DIR")]
    model: PathBuf,

    /// How receipts name the checkpoint; defaults to the encoder id in its config.
    #[arg(long, default_value = "")]
    model_id: String,

    /// Where to run: auto, cpu, mlx, vulkan or vulkan:N.
    #[arg(long, default_value = "auto")]
    device: String,

    /// Under auto, the slowest reference batch a device may take; 0 keeps the fastest device.
    #[arg(long, default_value_t = 0)]
    qualify_ms: u64,

    /// Where device verdicts and compiled GPU kernels persist across launches.
    #[arg(long, default_value = "")]
    cache_dir: String,

    /// MLX's Metal library (mlx.metallib) when it is not beside this executable.
    #[arg(long, default_value = "")]
    metallib: String,

    /// A head file, as NAME=PATH (turn-load, guide-load, unit-rank, code-rank or web-rank); repeatable.
    #[arg(long = "head", value_name = "NAME=PATH")]
    heads: Vec<String>,

    /// Rows per forward pass.
    #[arg(long, default_value_t = 0)]
    max_rows: usize,

    /// Precision: f16 or f32. Defaults to the device's own.
    #[arg(long, default_value = "")]
    dtype: String,

    /// Token budget for a question head (instructions and options); 0 keeps the checkpoint's.
    #[arg(long, default_value_t = 0)]
    head_max_len: usize,
}

#[derive(Subcommand)]
enum Command {
    /// Load the checkpoint and answer requests on stdin until it closes.
    Serve(LoadArgs),
    /// Load the checkpoint, run one warm-up pass, and print the engine identity as JSON.
    Check(LoadArgs),
    /// Time the reference batch on one explicit device and print it as JSON; `auto` runs this
    /// for each candidate. `--qualify-ms` stops at the first run slower than it.
    Probe(LoadArgs),
    /// Time ranking batches of the given size.
    Bench {
        #[command(flatten)]
        load: LoadArgs,
        /// Candidates per call.
        #[arg(long, default_value_t = 48)]
        candidates: usize,
        /// Calls to time after one warm-up.
        #[arg(long, default_value_t = 5)]
        runs: usize,
    },
}

/// Why the engine stopped, and the exit status that tells the host.
struct Failure {
    message: String,
    status: u8,
}

impl From<String> for Failure {
    fn from(message: String) -> Self {
        Failure { message, status: 1 }
    }
}

impl From<Error> for Failure {
    fn from(e: Error) -> Self {
        let status = match e {
            Error::NoCapableDevice(_) => EXIT_NO_CAPABLE_DEVICE,
            _ => 1,
        };
        Failure { message: e.to_string(), status }
    }
}

fn optional_path(s: &str) -> Option<PathBuf> {
    (!s.is_empty()).then(|| PathBuf::from(s))
}

fn options(args: &LoadArgs) -> Result<EngineOptions, String> {
    let device: DeviceChoice = args.device.parse().map_err(|e| format!("{e}"))?;
    let mut heads = Vec::with_capacity(args.heads.len());
    for spec in &args.heads {
        let (name, path) =
            spec.split_once('=').ok_or_else(|| format!("--head {spec:?}: expected NAME=PATH"))?;
        if !HEAD_NAMES.contains(&name) {
            return Err(format!("--head {spec:?}: the name must be one of {HEAD_NAMES:?}"));
        }
        heads.push((name.to_string(), PathBuf::from(path)));
    }
    Ok(EngineOptions {
        model_dir: args.model.clone(),
        model_id: args.model_id.clone(),
        device,
        heads,
        max_rows: args.max_rows,
        dtype: args.dtype.clone(),
        head_max_len: args.head_max_len,
        metallib: optional_path(&args.metallib),
        qualify: (args.qualify_ms > 0).then(|| Duration::from_millis(args.qualify_ms)),
        cache_dir: optional_path(&args.cache_dir),
        probe_exe: match device {
            DeviceChoice::Auto => std::env::current_exe().ok(),
            _ => None,
        },
    })
}

fn load(args: &LoadArgs) -> Result<Engine, Failure> {
    let opts = options(args)?;
    let started = Instant::now();
    let engine = Engine::load(&opts)?;
    engine.warm()?;
    let id = engine.identity();
    eprintln!(
        "bialy: ready model={} device={}{} dtype={} head={} load={:.1}s",
        id.model,
        id.device,
        if id.adapter.is_empty() { String::new() } else { format!(" ({})", id.adapter) },
        id.dtype,
        if id.head.is_empty() { "-" } else { &id.head },
        started.elapsed().as_secs_f64()
    );
    Ok(engine)
}

fn run(cli: Cli) -> Result<(), Failure> {
    match cli.command {
        Command::Serve(args) => {
            let engine = load(&args)?;
            let stdin = std::io::stdin();
            let stdout = std::io::stdout();
            bialy::protocol::serve(&engine, stdin.lock(), stdout.lock())
                .map_err(|e| Failure::from(e.to_string()))
        }
        Command::Check(args) => {
            let engine = load(&args)?;
            let identity = serde_json::to_string(engine.identity()).map_err(|e| e.to_string())?;
            println!("{identity}");
            Ok(())
        }
        Command::Probe(args) => {
            let opts = options(&args)?;
            if opts.device == DeviceChoice::Auto {
                return Err("probe measures one device; name it with --device".to_string().into());
            }
            let engine = Engine::load(&EngineOptions { qualify: None, heads: Vec::new(), ..opts.clone() })?;
            let elapsed = engine.probe_within(opts.qualify)?;
            let report = ProbeReport { probe_ms: elapsed.as_secs_f64() * 1000.0 };
            println!("{}", serde_json::to_string(&report).map_err(|e| e.to_string())?);
            Ok(())
        }
        Command::Bench { load: args, candidates, runs } => {
            let engine = load(&args)?;
            let batch = reference_candidates(candidates);
            engine.rank(HEAD_CODE_RANK, REFERENCE_TASK, &batch)?;
            let mut times = Vec::with_capacity(runs);
            for _ in 0..runs {
                let started = Instant::now();
                engine.rank(HEAD_CODE_RANK, REFERENCE_TASK, &batch)?;
                times.push(started.elapsed().as_secs_f64() * 1000.0);
            }
            times.sort_by(f64::total_cmp);
            println!(
                "candidates={candidates} runs={runs} p50={:.0}ms min={:.0}ms max={:.0}ms per_candidate={:.1}ms",
                times[times.len() / 2],
                times[0],
                times[times.len() - 1],
                times[times.len() / 2] / candidates as f64
            );
            Ok(())
        }
    }
}

fn main() -> ExitCode {
    match run(Cli::parse()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(failure) => {
            eprintln!("bialy: {}", failure.message);
            ExitCode::from(failure.status)
        }
    }
}
