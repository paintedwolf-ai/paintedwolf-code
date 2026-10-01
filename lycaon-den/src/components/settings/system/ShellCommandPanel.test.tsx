import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@solidjs/testing-library";

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...(args as [])),
}));

vi.mock("../../../platform/runtime.ts", () => ({
  isTauriRuntime: () => tauriRuntime,
}));

const invokeMock = vi.fn();
let tauriRuntime = true;

import { ShellCommandPanel } from "./ShellCommandPanel.tsx";

const baseStatus = {
  installed: false,
  bin_dir: "/usr/local/bin",
  pw_link: "/usr/local/bin/pw",
  pw_logs_link: "/usr/local/bin/pw-logs",
  pw_target: "/Applications/Painted Wolf Code.app/Contents/MacOS/pw",
  pw_logs_target: "/Applications/Painted Wolf Code.app/Contents/MacOS/pw-logs",
  unavailable_reason: null,
};

describe("ShellCommandPanel", () => {
  beforeEach(() => {
    tauriRuntime = true;
    invokeMock.mockReset();
    invokeMock.mockImplementation(async (cmd: string) => {
      if (cmd === "shell_command_status") return { ...baseStatus };
      if (cmd === "install_shell_command") {
        return { ...baseStatus, installed: true };
      }
      throw new Error(`unexpected ${cmd}`);
    });
  });

  it("loads status and installs", async () => {
    render(() => <ShellCommandPanel />);
    await waitFor(() => {
      expect(screen.getByTestId("shell-command-status").textContent).toContain(
        "Not installed",
      );
    });
    fireEvent.click(screen.getByTestId("shell-command-install"));
    await waitFor(() => {
      expect(screen.getByTestId("shell-command-status").textContent).toContain(
        "Installed",
      );
    });
    expect(invokeMock).toHaveBeenCalledWith("install_shell_command");
  });

  it("describes the desktop capability without invoking Tauri in browser mode", async () => {
    tauriRuntime = false;
    render(() => <ShellCommandPanel />);
    await waitFor(() => {
      expect(screen.getByTestId("shell-command-unavailable").textContent).toMatch(
        /desktop app/i,
      );
    });
    expect(screen.queryByTestId("shell-command-error")).toBeNull();
    expect(
      (screen.getByTestId("shell-command-install") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(invokeMock).not.toHaveBeenCalled();
  });
});
