mod app_state;
pub mod files;
mod notifications;
#[cfg(target_os = "linux")]
mod linux_desktop;
mod windows;
mod app_exit;
mod app_menu;
mod atomic_file;
mod backup_transfer;
mod clipboard;
mod config_dir;
mod detect_editors;
mod http_transport;
mod installation_mode;
mod linux_install;
mod open_external;
mod open_third_party_notices;
mod presence;
mod shell_command;
mod sidecar;
#[cfg(test)]
mod test_support;
pub mod update_service;
mod diagnostic_log;
mod vault_lock;
pub mod webkit_features;
mod webview_policy;
pub mod wheel_smoothing;

use sidecar::SidecarState;
use tauri::{Manager, RunEvent};

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let context = tauri::generate_context!();
    // GTK 3 names the Wayland toplevel and the X11 window class after the program
    // name, which must be the identifier the desktop entry is named after.
    #[cfg(target_os = "linux")]
    gtk::glib::set_prgname(Some(context.config().identifier.as_str()));
    // The launch guard runs before any window so an interrupted update finishes first.
    let launch = match update_service::transaction::launch() {
        Ok(launch) => launch,
        Err(error) => {
            rfd::MessageDialog::new()
                .set_title("Update recovery needed")
                .set_description(update_service::transaction::recovery_guidance(&error))
                .set_level(rfd::MessageLevel::Error)
                .show();
            return;
        }
    };
    #[cfg(target_os = "windows")]
    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_notification::init());
    #[cfg(not(target_os = "windows"))]
    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_shell::init());
    builder
        .plugin(webview_policy::init())
        .manage(SidecarState::new())
        .manage(clipboard::ClipboardState::default())
        .manage(app_exit::ExitCoordinator::default())
        .manage(backup_transfer::BackupTransferState::default())
        .manage(update_service::UpdateService::new(
            env!("PAINTED_WOLF_VERSION").to_string(),
            launch,
        ))
        .invoke_handler(tauri::generate_handler![
            app_exit::pending_app_exit,
            app_exit::acknowledge_app_exit,
            app_state::document_outbox::list_document_outbox,
            app_state::document_outbox::inspect_document_outbox,
            app_state::document_outbox::read_document_outbox,
            app_state::document_outbox::commit_document_outbox,
            app_state::read_app_state,
            app_state::patch_app_state,
            files::picked_file::save_bytes,
            backup_transfer::transfer_backup,
            backup_transfer::cancel_backup_transfer,
            notifications::den_notify_permission_state,
            notifications::den_notify_request_permission,
            notifications::den_notify_send,
            notifications::den_notify_cancel_sessions,
            notifications::den_set_badge_count,
            windows::accessibility_text_size::accessibility_preferred_text_size,
            windows::system_appearance::system_appearance,
            clipboard::read_clipboard_text,
            clipboard::write_clipboard_text,
            clipboard::paste_into_focused_view,
            files::reveal_in_file_manager::reveal_in_file_manager,
            files::reveal_in_file_manager::reveal_typed_path_in_file_manager,
            files::read_path_bytes::path_kind,
            files::read_path_bytes::import_path_kind,
            files::read_path_bytes::read_path_bytes,
            files::external_attachment_import::import_external_attachment,
            files::pick_path::pick_path,
            open_external::open_in_editor,
            open_external::open_in_browser,
            files::open_local_path::open_local_path,
            detect_editors::detect_editors,
            open_third_party_notices::open_third_party_notices,
            update_service::preferences::get_update_state,
            update_service::preferences::set_automatic_updates_enabled,
            update_service::preferences::set_update_channel,
            update_service::check::check_update,
            update_service::download::download_update,
            update_service::download::retry_update,
            update_service::transaction::restart_to_update,
            shell_command::shell_command_status,
            shell_command::install_shell_command,
            shell_command::uninstall_shell_command,
            windows::item_windows::ipc_open_item_window,
            windows::item_windows::drag::ipc_begin_file_item_window_drag,
            windows::item_windows::drag::ipc_move_item_window_drag,
            windows::item_windows::drag::ipc_finish_item_window_drag,
            windows::item_windows::drag::ipc_cancel_item_window_drag,
            windows::item_windows::drag::ipc_discard_pending_item_window_drags,
            windows::item_windows::drag::ipc_update_file_tab_drop_target,
            windows::item_windows::drag::ipc_clear_file_tab_drop_target,
            windows::item_windows::ipc_list_item_window_views,
            windows::item_windows::presentation::ipc_list_workspace_view_presentations,
            windows::item_windows::presentation::ipc_set_workspace_view_contexts,
            windows::item_windows::ipc_focus_item_window,
            windows::item_windows::ipc_close_item_window,
            app_state::shared_composer_documents::resolve_shared_composer_document,
            app_state::shared_composer_documents::acquire_shared_composer_document_lease,
            app_state::shared_composer_documents::update_shared_composer_document_draft,
            app_state::shared_composer_documents::apply_shared_composer_document_mutation,
            app_state::shared_composer_documents::protect_shared_composer_selection,
            app_state::shared_composer_documents::remove_shared_composer_document_attachment,
            app_state::shared_composer_documents::clear_shared_composer_document,
            sidecar::commands::ipc_start_sidecar,
            sidecar::commands::ipc_export_startup_diagnostics,
            sidecar::commands::ipc_restart_sidecar,
            sidecar::commands::ipc_reset_credential_vault,
            sidecar::commands::ipc_cancel_sidecar_start,
            sidecar::commands::ipc_attach_existing_daemon,
            sidecar::commands::ipc_sidecar_info,
            sidecar::commands::ipc_engine_state,
            presence::ipc_reveal_managed_secret,
            presence::ipc_resolve_checkpoint_with_presence,
            windows::window_appearance::den_reveal_window,
            windows::window_appearance::den_set_window_bounds,
            windows::window_backdrop::den_set_window_backdrop,
            app_menu::set_app_menu,
        ])
        .on_menu_event(|app, event| {
            app_menu::on_menu_event(app, event.id().as_ref());
        })
        .setup(|app| {
            backup_transfer::journal::cleanup_on_launch();
            app_state::document_outbox::reconcile_windows(app.handle(), None);
            sidecar::layout::setup_bundled_engine_layout(app.handle());
            sidecar::setup_supervision(app.handle());
            webkit_features::create_main_window(app.handle())?;
            windows::window_appearance::setup(app.handle());
            update_service::startup::start(app.handle().clone());
            windows::accessibility_text_size::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            windows::system_appearance::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            wheel_smoothing::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            vault_lock::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            notifications::setup(app.handle().clone())
                .map_err(|e| Box::<dyn std::error::Error>::from(e))?;
            Ok(())
        })
        .build(context)
        .expect("error while running tauri application")
        .run(|app, event| match event {
            RunEvent::ExitRequested { api, code, .. } => {
                if app_exit::allow_exit(app, code) {
                    windows::window_appearance::prepare_to_quit();
                } else {
                    api.prevent_exit();
                }
            }
            #[cfg(target_os = "macos")]
            RunEvent::Reopen {
                has_visible_windows: false,
                ..
            } => windows::window_appearance::restore_main_workspace(app),
            RunEvent::Exit => {
                notifications::shutdown(app);
                if let Some(state) = app.try_state::<clipboard::ClipboardState>() {
                    state.shutdown();
                }
                if let Some(state) = app.try_state::<SidecarState>() {
                    // Record why the unlocks ended before the engine stops.
                    vault_lock::lock_now(&state, vault_lock::LockReason::AppQuit);
                    sidecar::stop_sidecar(&state);
                }
            }
            _ => {}
        });
}


/// The installer mode never initializes windows, tools, or the engine.
pub fn run_update_helper(id: &str, installation: &str) -> Result<(), String> {
    update_service::initialize_helper(installation)?;
    update_service::transaction::run_helper(id).map_err(|e| e.to_string())
}
