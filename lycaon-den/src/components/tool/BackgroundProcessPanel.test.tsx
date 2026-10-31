import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
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
      expect(screen.getByText("Running")).toBeTruthy();
      expect(screen.queryByText("Exit code")).toBeNull();
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
      expect(screen.getByText("Exited")).toBeTruthy();
      expect(screen.getByText("Exit code").nextElementSibling?.textContent).toBe(String(exitCode));
      expect(screen.getByRole("button",{name:"Live output in Files"})).toBeTruthy();
      expect(screen.queryByText("phase")).toBeNull();
      expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    },
  );

  it("does not fabricate exit information for a missing process or reuse another chat's snapshot", () => {
    applyBackgroundProcessOutputs("first", [{ process_id: "process", running: false, exit_code: 7, chunks: [{ offset: 0, stream: "stdout", text: "old" }] }]);
    const [sessionId, setSessionId] = createSignal("first");
    render(() => <BackgroundProcessPanel sessionId={sessionId()} processId="process" />);
    expect(screen.getByText("Exit code").nextElementSibling?.textContent).toBe("7");
    setSessionId("second");
    expect(screen.queryByText("old")).toBeNull();
    expect(screen.queryByText("Exit code")).toBeNull();
    expect(screen.getByText("Checking…")).toBeTruthy();
    applyBackgroundProcessOutputs("second", []);
    expect(screen.getByText("No longer available")).toBeTruthy();
    expect(
      screen.getByText(
        "Background commands end when stopped or when Painted Wolf Code quits.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText("Exited")).toBeNull();
    expect(screen.queryByText("Exit code")).toBeNull();
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
    expect(screen.getByText("Stopped by you")).toBeTruthy();
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
    expect(
      screen.getByTestId("background-process-historical-reason").textContent,
    ).toBe("Ended when Painted Wolf Code quit");
    expect(screen.queryByText("No longer available")).toBeNull();
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
    expect(screen.getByText("Running")).toBeTruthy();

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
    expect(screen.getByText("No longer available")).toBeTruthy();
    expect(
      screen.getByTestId("background-process-unavailable-guidance").textContent,
    ).toBe(
      "Background commands end when stopped or when Painted Wolf Code quits.",
    );
    expect(
      screen.queryByText("Ended when Painted Wolf Code quit"),
    ).toBeNull();
  });
});
