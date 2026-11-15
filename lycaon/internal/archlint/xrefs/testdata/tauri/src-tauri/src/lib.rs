fn run() {
    builder.invoke_handler(tauri::generate_handler![
        app_exit::pending_app_exit,
        // retired: legacy_command,
        vault::vault_lock_status,
        #[cfg(target_os = "macos")]
        menu::open_menu,
    ]);
}
