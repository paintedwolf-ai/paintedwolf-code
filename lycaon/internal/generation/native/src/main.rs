//! `nativegen`: serve local generation to the host over stdio.

use clap::Parser;

/// Serve local generation to the host over stdio.
#[derive(Parser)]
#[command(name = "nativegen", version, about = "Painted Wolf Code local generation service")]
struct Args {
    /// Model directory with config.json and model.safetensors.
    #[arg(long)]
    model: String,
    /// Device: cpu or mlx.
    #[arg(long, default_value = "cpu")]
    device: String,
}

fn main() {
    let args = Args::parse();
    // Owned handles (not the thread-local std locks) so the reader thread can take stdin.
    if let Err(e) = nativegen::service::serve(std::io::stdin(), std::io::stdout()) {
        // Stdout is protocol-only after serve starts; the error goes to stderr and exits 1.
        eprintln!("nativegen: {e}");
        std::process::exit(1);
    }
    let _ = args;
}
