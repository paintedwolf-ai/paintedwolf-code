//! `bialy`: serve Bialy to the host over stdio.

use std::path::PathBuf;
use std::process::ExitCode;
use std::time::Instant;

use clap::{Parser, Subcommand};
use bialy::engine::{HEAD_CODE_RANK, HEAD_NAMES};
use bialy::{DeviceChoice, Engine, EngineOptions};

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

    /// Where to run: auto, cpu, cuda, cuda:N, metal, metal:N or mlx.
    #[arg(long, default_value = "auto")]
    device: String,

    /// MLX's Metal library (mlx.metallib) when it is not beside this executable.
    #[arg(long, default_value = "")]
    metallib: String,

    /// A head file, as NAME=PATH (turn-load, guide-load, unit-rank, code-rank or web-rank); repeatable.
    #[arg(long = "head", value_name = "NAME=PATH")]
    heads: Vec<String>,

    /// Rows per forward pass.
    #[arg(long, default_value_t = 0)]
    max_rows: usize,

    /// Precision: f16 or f32. Defaults to f16 on an accelerator and f32 on the CPU.
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
        metallib: if args.metallib.is_empty() {
            None
        } else {
            Some(std::path::PathBuf::from(&args.metallib))
        },
        head_max_len: args.head_max_len,
    })
}

fn load(args: &LoadArgs) -> Result<Engine, String> {
    let opts = options(args)?;
    let started = Instant::now();
    let engine = Engine::load(&opts).map_err(|e| e.to_string())?;
    engine.warm().map_err(|e| e.to_string())?;
    let id = engine.identity();
    eprintln!(
        "bialy: ready model={} device={} dtype={} head={} load={:.1}s",
        id.model,
        id.device,
        id.dtype,
        if id.head.is_empty() { "-" } else { &id.head },
        started.elapsed().as_secs_f64()
    );
    Ok(engine)
}

fn run(cli: Cli) -> Result<(), String> {
    match cli.command {
        Command::Serve(args) => {
            let engine = load(&args)?;
            let stdin = std::io::stdin();
            let stdout = std::io::stdout();
            bialy::protocol::serve(&engine, stdin.lock(), stdout.lock())
                .map_err(|e| e.to_string())
        }
        Command::Check(args) => {
            let engine = load(&args)?;
            println!("{}", serde_json::to_string(engine.identity()).map_err(|e| e.to_string())?);
            Ok(())
        }
        Command::Bench { load: args, candidates, runs } => {
            let engine = load(&args)?;
            let batch: Vec<String> = (0..candidates)
                .map(|i| {
                    format!(
                        "File: internal/search/code_executor_{i}.go (go)\nSymbol: rerankHits (function)\n\
                         func (e *CodeExecutor) rerankHits(ctx context.Context, terms []string, hits []Hit) []Hit\n\
                         Doc: rerankHits blends the engine's relevance into the lexical order of the top hits."
                    )
                })
                .collect();
            let task =
                "Where does project search blend the decision engine's relevance into its hits?";
            engine.rank(HEAD_CODE_RANK, task, &batch).map_err(|e| e.to_string())?;
            let mut times = Vec::with_capacity(runs);
            for _ in 0..runs {
                let started = Instant::now();
                engine.rank(HEAD_CODE_RANK, task, &batch).map_err(|e| e.to_string())?;
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
        Err(message) => {
            eprintln!("bialy: {message}");
            ExitCode::FAILURE
        }
    }
}
