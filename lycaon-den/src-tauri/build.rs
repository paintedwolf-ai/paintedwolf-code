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
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("linux") {
        build_toplevel_drag();
    }
    tauri_build::build()
}

/// Wayland tab tear-off records GTK's private objects as they are marshalled,
/// so linked shell artifacts export that one libwayland entry point.
fn build_toplevel_drag() {
    const SOURCE: &str = "src/windows/item_windows/toplevel_drag/toplevel_drag.c";
    println!("cargo:rerun-if-changed={SOURCE}");
    let wayland = pkg_config::Config::new()
        .cargo_metadata(false)
        .probe("wayland-client")
        .expect("wayland-client development files");
    let mut build = cc::Build::new();
    build.file(SOURCE).warnings(true).extra_warnings(true).warnings_into_errors(true);
    for path in &wayland.include_paths {
        build.include(path);
    }
    build.compile("pw_toplevel_drag");
    // After the archive, so the linker keeps the library it needs.
    pkg_config::Config::new()
        .probe("wayland-client")
        .expect("wayland-client development files");
    println!("cargo:rustc-link-arg=-Wl,--export-dynamic-symbol=wl_proxy_marshal_flags");
}
