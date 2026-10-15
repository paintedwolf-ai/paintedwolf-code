import { beforeEach, describe, expect, it, vi } from "vitest";

let tauri = true;
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  isTauriRuntime: () => tauri,
}));

const invoke = vi.fn(async (_command: string): Promise<unknown> => undefined);
vi.mock("@tauri-apps/api/core", () => ({
  invoke: (command: string) => invoke(command),
}));

const { loadShellCommandPackaged, shellCommandPackaged, shellCommandStatus } = await import("./shell-command.ts");

function status(managedByPackage: boolean) {
  return {
    installed: managedByPackage,
    installation_mode: managedByPackage ? "package" : "bundle",
    managed_by_package: managedByPackage,
    bin_dir: "/usr/bin",
    pw_link: "/usr/bin/pw",
    pw_logs_link: "/usr/bin/pw-logs",
    pw_target: null,
    pw_logs_target: null,
    unavailable_reason: null,
  };
}

beforeEach(() => {
  tauri = true;
  invoke.mockReset();
});

describe("shellCommandPackaged", () => {
  it("follows the host's report of who owns the command", async () => {
    invoke.mockResolvedValueOnce(status(true));
    await loadShellCommandPackaged();
    expect(invoke).toHaveBeenCalledWith("shell_command_status");
    expect(shellCommandPackaged()).toBe(true);

    invoke.mockResolvedValueOnce(status(false));
    await shellCommandStatus();
    expect(shellCommandPackaged()).toBe(false);
  });

  it("keeps the last answer when the host cannot report", async () => {
    invoke.mockResolvedValueOnce(status(true));
    await loadShellCommandPackaged();
    invoke.mockRejectedValueOnce(new Error("gone"));
    await loadShellCommandPackaged();
    expect(shellCommandPackaged()).toBe(true);
  });

  it("asks nothing outside the desktop runtime", async () => {
    tauri = false;
    await loadShellCommandPackaged();
    expect(invoke).not.toHaveBeenCalled();
  });
});
