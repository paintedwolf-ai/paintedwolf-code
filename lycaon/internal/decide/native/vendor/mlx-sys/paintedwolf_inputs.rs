// Painted Wolf Code modification: declare offline inputs and keep CMake's
// source/toolchain paths inside this build's persistent output directory.
use std::{env, fs, io, path::Path};

fn remove_entry(path: &Path) -> io::Result<()> {
    match fs::symlink_metadata(path) {
        Ok(info) if info.is_dir() => fs::remove_dir_all(path),
        Ok(_) => fs::remove_file(path),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error),
    }
}

fn copy_file(source: &Path, target: &Path) -> io::Result<()> {
    let contents = fs::read(source)?;
    let same = fs::symlink_metadata(target)
        .is_ok_and(|info| info.is_file() && fs::read(target).is_ok_and(|previous| previous == contents));
    if !same {
        remove_entry(target)?;
        fs::copy(source, target)?;
    }
    fs::set_permissions(target, fs::metadata(source)?.permissions())
}

fn stage_tree(source: &Path, target: &Path) -> io::Result<()> {
    if fs::symlink_metadata(target).is_ok_and(|info| !info.is_dir()) {
        remove_entry(target)?;
    }
    fs::create_dir_all(target)?;
    for entry in fs::read_dir(target)? {
        let entry = entry?;
        match fs::symlink_metadata(source.join(entry.file_name())) {
            Err(error) if error.kind() == io::ErrorKind::NotFound => remove_entry(&entry.path())?,
            Err(error) => return Err(error),
            Ok(_) => {}
        }
    }
    for entry in fs::read_dir(source)? {
        let entry = entry?;
        let destination = target.join(entry.file_name());
        let kind = entry.file_type()?;
        if kind.is_dir() {
            stage_tree(&entry.path(), &destination)?;
        } else if kind.is_file() {
            copy_file(&entry.path(), &destination)?;
        } else if kind.is_symlink() {
            let link = fs::read_link(entry.path())?;
            if fs::read_link(&destination).ok().as_ref() != Some(&link) {
                remove_entry(&destination)?;
                #[cfg(unix)]
                std::os::unix::fs::symlink(link, destination)?;
                #[cfg(not(unix))]
                return Err(io::Error::new(io::ErrorKind::Unsupported, "MLX source symlink"));
            }
        } else {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "MLX source entry type"));
        }
    }
    fs::set_permissions(target, fs::metadata(source)?.permissions())
}

pub fn configure(source: &Path) -> cmake::Config {
    let manifest = env::var_os("CARGO_MANIFEST_DIR").expect("Cargo manifest directory");
    let manifest = Path::new(&manifest);
    let root = manifest.ancestors().nth(6).expect("repository root");
    let vendor = root.join("third_party/mlx");
    let toolchain = root.join("scripts/toolchain/mlx-offline.cmake");
    println!("cargo:rerun-if-changed={}", vendor.display());
    println!("cargo:rerun-if-changed={}", source.display());
    println!("cargo:rerun-if-changed={}", toolchain.display());

    let output = env::var_os("OUT_DIR").expect("Cargo build output directory");
    let inputs = Path::new(&output).join("paintedwolf-inputs");
    let staged_source = inputs.join("mlx-c");
    stage_tree(source, &staged_source).expect("stage persistent MLX CMake source");
    let staged_toolchain = inputs.join("mlx-offline.cmake");
    copy_file(&toolchain, &staged_toolchain).expect("stage persistent MLX CMake toolchain");
    let mut config = cmake::Config::new(&staged_source);
    config.define("CMAKE_TOOLCHAIN_FILE", staged_toolchain);
    config.define("PW_MLX_VENDOR_ROOT", &vendor);
    config.define("PW_MLX_SYS_MANIFEST", manifest.join("Cargo.toml"));
    config
}
