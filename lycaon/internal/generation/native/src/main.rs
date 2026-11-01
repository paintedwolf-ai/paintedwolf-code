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
    #[arg(long)]
    device_pci: Option<String>,
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
    let mut runner = Runner::new(
        args.model,
        identity,
        |path: &std::path::Path, _, _, budget| Mlx::load(path, budget as usize),
    )?;
    runner.serve(std::io::stdin().lock(), std::io::stdout().lock())
}

#[cfg(all(feature = "vulkan", target_os = "linux"))]
fn run(args: Args) -> nativegen::Result<()> {
    use nativegen::{
        Error,
        runner::{Identity, Runner, wire::PhysicalDevice},
        vulkan::Vulkan,
    };
    let pci = args
        .device_pci
        .ok_or_else(|| Error::Protocol("Vulkan PCI device is required".into()))?;
    let identity = Identity {
        artifact_id: args.artifact_id,
        backend: "vulkan",
        physical_device: PhysicalDevice::Pci {
            address: pci.clone(),
        },
    };
    let mut runner = Runner::new(
        args.model,
        identity,
        move |path: &std::path::Path, context, chunk, budget| {
            Vulkan::load(path, &pci, context, chunk, budget)
        },
    )?;
    runner.serve(std::io::stdin().lock(), std::io::stdout().lock())
}

#[cfg(not(any(
    all(feature = "mlx", target_os = "macos", target_arch = "aarch64"),
    all(feature = "vulkan", target_os = "linux")
)))]
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
