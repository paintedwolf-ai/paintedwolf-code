fn main() {
    println!("cargo:rerun-if-env-changed=PW_GENERATION_VULKAN_LIB_DIR");
    if std::env::var_os("CARGO_FEATURE_VULKAN").is_some() {
        assert_eq!(std::env::var("CARGO_CFG_TARGET_OS").unwrap(), "linux");
        let directory = std::env::var("PW_GENERATION_VULKAN_LIB_DIR")
            .expect("prepare the Vulkan adapter through generation:build");
        println!("cargo:rustc-link-search=native={directory}");
        println!("cargo:rustc-link-lib=dylib=nativegen_vulkan");
        println!("cargo:rustc-link-arg=-Wl,-rpath,$ORIGIN");
        println!("cargo:rustc-link-arg=-Wl,-rpath,$ORIGIN/../engine-root/generation");
    }
}
