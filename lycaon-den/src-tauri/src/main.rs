// Suppresses the release console window.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    let mut args = std::env::args().skip(1);
    if args.next().as_deref() == Some("--apply-update") {
        let id = args.next().unwrap_or_default();
        if args.next().is_some() || painted_wolf_code_lib::run_update_helper(&id).is_err() {
            std::process::exit(1);
        }
        return;
    }
    painted_wolf_code_lib::run()
}
