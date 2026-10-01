import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearEngineStartupProgress,
  noteEngineStartupProgress,
} from "../platform/connection/engine-startup.ts";
import { EngineStartupStage } from "./EngineStartupStage.tsx";


const checkNativeUpdate = vi.fn();
const installNativeUpdate = vi.fn();
const nativeUpdateState = {
  revision: 0, phase: "idle", current_version: "1.0.0", channel: "stable",
  install_source: "direct_download", checks_enabled: false,
  rollout_eligibility: "not_applicable", downloaded_bytes: 0, total_bytes: null,
};
vi.mock("../settings/system/update-service.ts", () => ({
  nativeUpdateService: {
    getState: async () => nativeUpdateState,
    subscribe: async () => () => {},
    check: () => checkNativeUpdate(),
    install: (version: string) => installNativeUpdate(version),
  },
}));

const cancelBackendStart = vi.fn();
const saveStartupDiagnosticsBundle = vi.fn();

vi.mock("../platform/connection/backend.ts", () => ({
  cancelBackendStart: () => cancelBackendStart(),
}));

vi.mock("../settings/system/diagnostics-export.ts", () => ({
  saveStartupDiagnosticsBundle: () => saveStartupDiagnosticsBundle(),
}));

vi.mock("../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => true,
  tauriDragRegionProps: (options?: { deep?: boolean }) => ({
    "data-tauri-drag-region": options?.deep ? "deep" : "",
  }),
}));

const componentsCss = readSourceText(
  join(import.meta.dirname, "..", "global-components.css"),
  "utf8",
);

const base = {
  protocol: 1,
  status: "starting" as const,
  phase: "providers" as const,
  pid: 42,
  sequence: 4,
  elapsed_ms: 9_400,
  silence_ms: 0,
};

describe("EngineStartupStage", () => {
  beforeEach(() => {
    checkNativeUpdate.mockReset();
    installNativeUpdate.mockReset();
    checkNativeUpdate.mockResolvedValue({ ...nativeUpdateState, phase: "available", available_version: "1.1.0" });
    installNativeUpdate.mockResolvedValue({ ...nativeUpdateState, phase: "restart_required", available_version: "1.1.0" });

    cancelBackendStart.mockReset();
    cancelBackendStart.mockResolvedValue(true);
    saveStartupDiagnosticsBundle.mockReset();
    saveStartupDiagnosticsBundle.mockResolvedValue({
      kind: "saved",
      path: "/tmp/startup.zip",
    });
    clearEngineStartupProgress();
  });

  afterEach(() => clearEngineStartupProgress());

  it("shows live semantic progress without presenting a failure", () => {
    noteEngineStartupProgress(base);
    render(() => <EngineStartupStage />);

    const stage = screen.getByTestId("engine-startup");
    expect(stage.getAttribute("data-status")).toBe("starting");
    expect(stage.textContent).toContain("Starting Painted Wolf Code");
    expect(screen.getByTestId("engine-startup-phase").textContent).toBe(
      "Loading AI providers",
    );
    expect(screen.getByTestId("engine-startup-phase").getAttribute("role")).toBe(
      "status",
    );
    expect(screen.queryByTestId("engine-startup-stop")).toBeNull();
  });

  it("drags the window from the startup copy while its controls stay clickable", () => {
    noteEngineStartupProgress({ ...base, elapsed_ms: 30_000 });
    render(() => <EngineStartupStage />);

    expect(
      screen.getByTestId("engine-startup").getAttribute("data-tauri-drag-region"),
    ).toBe("deep");
    expect(componentsCss).toMatch(
      /\.den-engine-startup\[data-tauri-drag-region\][\s\S]*?-webkit-app-region:\s*drag/,
    );
    expect(componentsCss).toMatch(
      /\.den-engine-startup__actions\s*\{[\s\S]*?-webkit-app-region:\s*no-drag/,
    );
  });

  it("offers cancellation for a long startup that is still reporting progress", () => {
    noteEngineStartupProgress({ ...base, elapsed_ms: 30_000 });
    render(() => <EngineStartupStage />);

    expect(screen.getByTestId("engine-startup-stop")).toBeTruthy();
    expect(screen.getByTestId("engine-startup").textContent).toContain(
      "making progress",
    );
  });

  it("keeps waiting on a stalled child and offers explicit controls", async () => {
    noteEngineStartupProgress({
      ...base,
      status: "stalled",
      sequence: 5,
      elapsed_ms: 15_000,
      silence_ms: 5_100,
    });
    render(() => <EngineStartupStage />);

    expect(screen.getByTestId("engine-startup").textContent).toContain(
      "Waiting will continue until you choose to stop it.",
    );
    fireEvent.click(screen.getByTestId("engine-startup-stop"));
    await waitFor(() => expect(cancelBackendStart).toHaveBeenCalledOnce());
  });

  it("updates while the engine never becomes ready and automatic checks are off", async () => {
    noteEngineStartupProgress({ ...base, status: "stalled", silence_ms: 5_100 });
    render(() => <EngineStartupStage />);
    expect(screen.getByTestId("recovery-updates")).toBeTruthy();
    await waitFor(() => expect((screen.getByTestId("updates-check-now") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByTestId("updates-check-now"));
    await waitFor(() => expect(screen.getByTestId("updates-install")).toBeTruthy());
    fireEvent.click(screen.getByTestId("updates-install"));
    await waitFor(() => expect(installNativeUpdate).toHaveBeenCalledWith("1.1.0"));
    expect(cancelBackendStart).not.toHaveBeenCalled();
  });

  it("saves offline startup diagnostics while the child is stalled", async () => {
    noteEngineStartupProgress({
      ...base,
      status: "stalled",
      sequence: 5,
      silence_ms: 5_100,
    });
    render(() => <EngineStartupStage />);

    fireEvent.click(screen.getByTestId("engine-startup-save"));
    await waitFor(() => {
      expect(saveStartupDiagnosticsBundle).toHaveBeenCalledOnce();
      expect(screen.getByTestId("engine-startup-status").textContent).toContain(
        "Saved to /tmp/startup.zip.",
      );
    });
  });
});
