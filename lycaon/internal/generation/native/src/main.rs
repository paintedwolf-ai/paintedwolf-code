use clap::Parser;
use std::path::PathBuf;

#[derive(Parser)]
#[command(
    name = "nativegen",
    version,
    about = "Painted Wolf Code local generation service"
)]
struct Args {
    #[arg(long)]
    model: PathBuf,
    #[arg(long)]
    artifact_id: String,
    #[arg(long)]
    metallib: Option<PathBuf>,
}

#[cfg(all(feature = "mlx", target_os = "macos", target_arch = "aarch64"))]
fn run(args: Args) -> nativegen::Result<()> {
    use nativegen::{
        error::Error,
        mlx_engine::backend::Mlx,
        runner::{Identity, Runner, wire::PhysicalDevice},
    };
    let shader = args
        .metallib
        .ok_or_else(|| Error::Protocol("Metal library path is required".into()))?;
    if !shader.is_absolute() || !shader.is_file() {
        return Err(Error::Protocol(
            "Metal library path must name an absolute file".into(),
        ));
    }
    mlx_rs::metal::set_metallib_path(shader.to_string_lossy())
        .map_err(|e| Error::Device(e.to_string()))?;
    let identity = Identity {
        artifact_id: args.artifact_id,
        backend: "mlx",
        physical_device: PhysicalDevice::AppleSilicon,
    };
    let mut runner = Runner::new(args.model, identity, |path: &std::path::Path, _, _| {
        Mlx::load(path)
    })?;
    runner.serve(std::io::stdin().lock(), std::io::stdout().lock())
}

#[cfg(not(all(feature = "mlx", target_os = "macos", target_arch = "aarch64")))]
fn run(_args: Args) -> nativegen::Result<()> {
    Err(nativegen::Error::Device(
        "no generation adapter was built for this platform".into(),
    ))
}

fn main() {
    if let Err(error) = run(Args::parse()) {
        eprintln!("nativegen: {error}");
        std::process::exit(1);
    }
}
