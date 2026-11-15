import { invoke } from "@tauri-apps/api/core";
type Command = "open_menu";
export const pending = () => invoke<boolean>("pending_app_exit");
export const gone = () => invoke("removed_command");
export const menu = (c: Command) => invoke(c);
