use clap::Parser;
use std::path::PathBuf;

#[derive(Parser)]
#[command(
    name = "nativegen",
    version,
    about = "Painted Wolf Code local generation service"
)]
struct Args {
    #[arg(long, conflicts_with_all = ["model", "artifact_id", "device_pci"])]
    inventory: bool,
    #[arg(long, required_unless_present = "inventory")]
    model: Option<PathBuf>,
    #[arg(long, required_unless_present = "inventory")]
    artifact_id: Option<String>,
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
    if args.inventory {
        return print_inventory();
    }
    let identity = Identity {
        artifact_id: args.artifact_id.expect("required by CLI"),
        backend: "mlx",
        physical_device: PhysicalDevice::AppleSilicon,
    };
    let mut runner = Runner::new(
        args.model.expect("required by CLI"),
        identity,
        |path: &std::path::Path, context, chunk, budget| {
            Mlx::load(path, context, chunk, budget as usize)
        },
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
    if args.inventory {
        return print_inventory();
    }
    let pci = args
        .device_pci
        .ok_or_else(|| Error::Protocol("Vulkan PCI device is required".into()))?;
    let identity = Identity {
        artifact_id: args.artifact_id.expect("required by CLI"),
        backend: "vulkan",
        physical_device: PhysicalDevice::Pci {
            address: pci.clone(),
        },
    };
    let mut runner = Runner::new(
        args.model.expect("required by CLI"),
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

fn print_inventory() -> nativegen::Result<()> {
    let inventory = nativegen::inventory::inspect()?;
    serde_json::to_writer(std::io::stdout().lock(), &inventory)
        .map_err(|error| nativegen::Error::Protocol(error.to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn inventory_needs_no_model_and_cannot_select_one() {
        assert!(Args::try_parse_from(["nativegen", "--inventory"]).is_ok());
        assert!(Args::try_parse_from(["nativegen", "--inventory", "--model", "/weights"]).is_err());
        assert!(Args::try_parse_from(["nativegen", "--model", "/weights"]).is_err());
        assert!(
            Args::try_parse_from(["nativegen", "--model", "/weights", "--artifact-id", "hash"])
                .is_ok()
        );
    }
}

fn main() {
    if let Err(error) = run(Args::parse()) {
        eprintln!("nativegen: {error}");
        std::process::exit(1);
    }
}
