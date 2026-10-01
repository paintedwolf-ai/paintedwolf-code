// Suppresses the release console window.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    painted_wolf_code_lib::run()
}
