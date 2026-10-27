use std::path::PathBuf;

fn main() {
    println!("cargo:rerun-if-env-changed=PW_REQUIRE_RELEASE_PROTOCOL");
    if let Some(required) = std::env::var_os("PW_REQUIRE_RELEASE_PROTOCOL") {
        assert_eq!(required, "1", "Invalid release protocol requirement");
        assert!(!tauri_build::is_dev(), "The packaged desktop shell requires Tauri custom-protocol");
    }
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
