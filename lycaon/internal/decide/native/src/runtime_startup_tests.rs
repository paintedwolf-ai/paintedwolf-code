use std::path::PathBuf;
use std::process::Command;

use crate::memory::Limits;
use crate::testutil::{scratch_dir, write_tiny_checkpoint};
use crate::{DeviceChoice, Engine, EngineOptions};

#[test]
fn packaged_mlx_library_is_selected_before_allocator_startup() {
    const CHILD: &str = "BIALY_TEST_PACKAGED_MLX_STARTUP";
    if std::env::var_os(CHILD).is_some() {
        let dir = scratch_dir("packaged-mlx-startup");
        write_tiny_checkpoint(&dir);
        crate::mlx::set_metallib(&dir.join("unavailable.metallib")).unwrap();
        let metallib = PathBuf::from(std::env::var_os("MLX_RS_METAL_PATH").unwrap())
            .join("mlx.metallib");
        assert!(metallib.is_file());
        let engine = Engine::load(&EngineOptions {
            model_dir: dir.to_path_buf(),
            device: DeviceChoice::Mlx,
            metallib: Some(metallib),
            memory: Limits { resident: 256 << 20, peak: 512 << 20 },
            ..Default::default()
        }).unwrap();
        assert_eq!(engine.identity().device, "mlx");
        return;
    }

    // A fresh process prevents another test's initialized device from hiding the load order.
    let output = Command::new(std::env::current_exe().unwrap())
        .args([
            "--exact",
            "runtime_startup_tests::packaged_mlx_library_is_selected_before_allocator_startup",
            "--nocapture",
        ])
        .env(CHILD, "1")
        .output()
        .unwrap();
    assert!(output.status.success(), "child {}\n{}\n{}",
        output.status, String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr));
}
