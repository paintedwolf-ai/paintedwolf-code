#[cfg(target_os = "linux")]
mod portal;
fn main() {
    if std::env::args_os().len() != 1 {
        std::process::exit(2);
    }
    #[cfg(target_os = "linux")]
    if desktop_settings_observer::harden().is_err() || portal::run().is_err() {
        std::process::exit(1);
    }
    #[cfg(not(target_os = "linux"))]
    std::process::exit(2);
}
