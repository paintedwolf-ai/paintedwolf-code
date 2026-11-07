import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { assertNamedControl } from "../../test/controls.ts";
import { createSignal } from "solid-js";
import {
  applyBackgroundProcessEvent,
  applyBackgroundProcessOutputs,
  refreshBackgroundProcessSnapshots,
  resetBackgroundProcessStoreForTests,
} from "../../chat/tool/background-process-store.ts";
import { BackgroundProcessPanel } from "./BackgroundProcessPanel.tsx";

vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => null }));

describe("background process outcomes", () => {
  beforeEach(resetBackgroundProcessStoreForTests);

  it.each([0, 7, -1].flatMap((exitCode) => ["event", "bootstrap", "reload"].map((source) => ({ exitCode, source }))))(
    "shows exit $exitCode from $source with a link to its output",
    async ({ exitCode, source }) => {
      render(() => <BackgroundProcessPanel sessionId="session" processId="process" initialRunning />);
      expect(screen.getByTestId("background-process-running")).toBeTruthy();
      expect(screen.queryByTestId("background-process-exit-code")).toBeNull();
      const output = {
        process_id: "process", running: false, exit_code: exitCode,
        chunks: [{ offset: 0, stream: "stdout", text: "phase\n" }],
      };
      if (source === "event") {
        applyBackgroundProcessEvent({ session_id: "session", process_id: "process", running: true, stream: "stdout", end_offset: 0, text: "phase\n" });
        applyBackgroundProcessEvent({ session_id: "session", process_id: "process", running: false, stream: "exit", end_offset: 6, exit_code: exitCode });
      } else if (source === "bootstrap") {
        applyBackgroundProcessOutputs("session", [output]);
      } else {
        await refreshBackgroundProcessSnapshots({
          listSessionBackgroundProcesses: async () => [{ process_id: "process", running: false, exit_code: exitCode, output }],
          getSessionBackgroundProcessOutput: async () => output,
        }, "session");
      }
      expect(screen.getByTestId("background-process-ended")).toBeTruthy();
      expect(screen.getByTestId("background-process-exit-code").textContent).toBe(String(exitCode));
      expect(assertNamedControl(screen.getByTestId("tool-content-link"), "button")).toBeTruthy();
      expect(screen.queryByText("phase")).toBeNull();
      expect(screen.queryByTestId("background-process-stop")).toBeNull();
    },
  );

  it("does not fabricate exit information for a missing process or reuse another chat's snapshot", () => {
    applyBackgroundProcessOutputs("first", [{ process_id: "process", running: false, exit_code: 7, chunks: [{ offset: 0, stream: "stdout", text: "old" }] }]);
    const [sessionId, setSessionId] = createSignal("first");
    render(() => <BackgroundProcessPanel sessionId={sessionId()} processId="process" />);
    expect(screen.getByTestId("background-process-exit-code").textContent).toBe("7");
    setSessionId("second");
    expect(screen.queryByText("old")).toBeNull();
    expect(screen.queryByTestId("background-process-exit-code")).toBeNull();
    applyBackgroundProcessOutputs("second", []);
    expect(screen.getByTestId("background-process-unavailable")).toBeTruthy();
    expect(screen.getByTestId("background-process-unavailable-guidance")).toBeTruthy();
    expect(screen.queryByTestId("background-process-ended")).toBeNull();
    expect(screen.queryByTestId("background-process-exit-code")).toBeNull();
  });

  it("renders the recorded termination reason for ended commands", () => {
    render(() => (
      <BackgroundProcessPanel
        sessionId="session"
        processId="process"
        initialRunning
      />
    ));
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "process",
      running: false,
      stream: "exit",
      end_offset: 0,
      exit_code: -1,
      termination_reason: "stopped_by_user",
    });
  });

  it("shows 'Ended when Painted Wolf Code quit' for a historical card with a recorded app_quit reason", () => {
    applyBackgroundProcessOutputs("session", []);
    render(() => (
      <BackgroundProcessPanel
        sessionId="session"
        processId="process-quit"
        terminationReason="app_quit"
      />
    ));
    expect(screen.getByTestId("background-process-historical-reason")).toBeTruthy();
    expect(screen.queryByTestId("background-process-unavailable")).toBeNull();
  });

  it("uses neutral unavailable copy after same-run pruning or unexpected engine restart without relabeling as app_quit", async () => {
    render(() => (
      <BackgroundProcessPanel
        sessionId="session"
        processId="process-pruned"
        initialRunning
      />
    ));
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "process-pruned",
      running: true,
      stream: "stdout",
      end_offset: 3,
      text: "ok\n",
    });
    expect(screen.getByTestId("background-process-running")).toBeTruthy();

    // Simulate unexpected engine restart or same-run pruning where hydrated session snapshot no longer has the handle.
    await refreshBackgroundProcessSnapshots(
      {
        listSessionBackgroundProcesses: async () => [],
        getSessionBackgroundProcessOutput: async () => ({
          process_id: "process-pruned",
          running: false,
          chunks: [],
        }),
      },
      "session",
    );
    expect(screen.getByTestId("background-process-unavailable")).toBeTruthy();
    expect(screen.getByTestId("background-process-unavailable-guidance")).toBeTruthy();
    expect(screen.queryByTestId("background-process-historical-reason")).toBeNull();
  });
});
