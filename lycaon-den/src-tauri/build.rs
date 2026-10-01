use std::path::PathBuf;

fn main() {
    let manifest = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").expect("manifest dir"));
    let version_path = manifest.join("../../VERSION");
    let version = std::fs::read_to_string(&version_path)
        .expect("read repository VERSION")
        .trim()
        .to_string();
    semver::Version::parse(&version).expect("VERSION must be complete semantic versioning");
    println!("cargo:rerun-if-changed={}", version_path.display());
    println!("cargo:rustc-env=PAINTED_WOLF_VERSION={version}");
    tauri_build::build()
}
