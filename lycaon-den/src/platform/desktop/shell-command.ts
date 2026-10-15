import { createSignal } from "solid-js";
import { isTauriRuntime } from "../runtime.ts";

export type ShellCommandStatus = {
  installed: boolean;
  installation_mode: "package" | "bundle" | "developer";
  /** The system package owns the entry points; the app never writes them. */
  managed_by_package: boolean;
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
    installation_mode: "developer",
    managed_by_package: false,
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

const [packaged, setPackaged] = createSignal(false);

/** The system package owns the pw command, so the app offers no install. */
export const shellCommandPackaged = packaged;

export async function shellCommandStatus(): Promise<ShellCommandStatus> {
  if (!isTauriRuntime()) return unavailableStatus();
  const status = await invokeShellCommand("shell_command_status");
  setPackaged(status.managed_by_package);
  return status;
}

/** Reads at startup whether a package provides the command. */
export async function loadShellCommandPackaged(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    await shellCommandStatus();
  } catch (err) {
    console.debug("[shell-command] status unavailable", err);
  }
}

export async function installShellCommand(): Promise<ShellCommandStatus> {
  return await invokeShellCommand("install_shell_command");
}

export async function uninstallShellCommand(): Promise<ShellCommandStatus> {
  return await invokeShellCommand("uninstall_shell_command");
}
