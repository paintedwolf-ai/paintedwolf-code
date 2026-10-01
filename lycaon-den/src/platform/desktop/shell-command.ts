import { isTauriRuntime } from "../runtime.ts";

export type ShellCommandStatus = {
  installed: boolean;
  bin_dir: string;
  pw_link: string;
  pw_logs_link: string;
  pw_target: string | null;
  pw_logs_target: string | null;
  unavailable_reason: string | null;
};

const DESKTOP_ONLY_REASON = "The command-line tool is available in the desktop app.";

function unavailableStatus(): ShellCommandStatus {
  return {
    installed: false,
    bin_dir: "",
    pw_link: "",
    pw_logs_link: "",
    pw_target: null,
    pw_logs_target: null,
    unavailable_reason: DESKTOP_ONLY_REASON,
  };
}

async function invokeShellCommand(command: string): Promise<ShellCommandStatus> {
  if (!isTauriRuntime()) throw new Error(DESKTOP_ONLY_REASON);
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<ShellCommandStatus>(command);
}

export async function shellCommandStatus(): Promise<ShellCommandStatus> {
  if (!isTauriRuntime()) return unavailableStatus();
  return await invokeShellCommand("shell_command_status");
}

export async function installShellCommand(): Promise<ShellCommandStatus> {
  return await invokeShellCommand("install_shell_command");
}

export async function uninstallShellCommand(): Promise<ShellCommandStatus> {
  return await invokeShellCommand("uninstall_shell_command");
}
