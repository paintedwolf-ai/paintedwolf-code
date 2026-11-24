import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  resetHomeLaunchesForTest,
  setLaunchRuntime,
  submitHomeLaunch,
  updateLaunch,
} from "../../home/launch/home-launches.ts";
import { memoryHomeOutbox } from "../../home/launch/home-outbox.ts";
import type { LaunchChip } from "../../home/launch/home-launch-model.ts";
import type { HomeLaunchController } from "../shell/home-launch-controller.ts";
import { LaunchConversation } from "./LaunchConversation.tsx";

function controller(): HomeLaunchController {
  return {
    load: vi.fn(), restoredComposition: () => undefined, submit: vi.fn(), open: vi.fn(), stop: vi.fn(),
    discard: vi.fn(), retry: vi.fn(), retrySaving: vi.fn(), edit: vi.fn(), editOnHome: vi.fn(),
    startAgain: vi.fn(), replaceChip: vi.fn(), acceptChangedFile: vi.fn(), removeChip: vi.fn(),
    sendHere: vi.fn(), openChat: vi.fn(),
  } as unknown as HomeLaunchController;
}

function launch(chips: LaunchChip[] = []): string {
  return submitHomeLaunch(
    { text: "Build a tic tac game with AI I can beat", mode: "agent", chips, compositionId: "c", compositionRevision: 1 },
    () => undefined,
  );
}

beforeEach(() => resetHomeLaunchesForTest(memoryHomeOutbox()));
afterEach(cleanup);

describe("LaunchConversation", () => {
  it("shows the idea as a pending row, takes focus and names the conversation", () => {
    const id = launch();
    render(() => <LaunchConversation launchId={id} controller={controller()} onOpenProviders={vi.fn()} />);
    const region = screen.getByRole("region", { name: "New chat" });
    expect(document.activeElement).toBe(region);
    const bubble = screen.getByTestId("pending-send-bubble");
    expect(bubble.textContent).toContain("Build a tic tac game with AI I can beat");
    expect(bubble.getAttribute("data-operation-id")).toBe(id);
  });

  it("routes each blocker's actions to the controller", () => {
    const id = launch();
    const actions = controller();
    const onOpenProviders = vi.fn();
    render(() => <LaunchConversation launchId={id} controller={actions} onOpenProviders={onOpenProviders} />);

    setLaunchRuntime(id, { blocker: { code: "engine_offline" } });
    fireEvent.click(screen.getByTestId("launch-action-retry"));
    expect(actions.retry).toHaveBeenCalledWith(id);

    setLaunchRuntime(id, { blocker: { code: "provider", message: "No model is configured." } });
    fireEvent.click(screen.getByTestId("launch-action-open_settings"));
    expect(onOpenProviders).toHaveBeenCalledOnce();

    setLaunchRuntime(id, { blocker: { code: "destination_changed" } });
    fireEvent.click(screen.getByTestId("launch-action-send_here"));
    expect(actions.sendHere).toHaveBeenCalledWith(id);

    setLaunchRuntime(id, { blocker: { code: "stopped" } });
    expect(screen.queryByTestId("launch-stop")).toBeNull();
    fireEvent.click(screen.getByTestId("launch-action-edit_on_home"));
    expect(actions.editOnHome).toHaveBeenCalledWith(id);
  });

  it("explains a failed save beside the idea with a retry", () => {
    const id = launch();
    const actions = controller();
    render(() => <LaunchConversation launchId={id} controller={actions} onOpenProviders={vi.fn()} />);
    setLaunchRuntime(id, { save: "failed", saveError: "The disk is full." });
    expect(screen.getByTestId("launch-save-notice").textContent).toContain("The disk is full.");
    fireEvent.click(screen.getByTestId("launch-action-retry_saving"));
    expect(actions.retrySaving).toHaveBeenCalledWith(id);
  });

  it("marks only the chips that need a person and offers their remedies", () => {
    const missing: LaunchChip = { kind: "bytes", itemId: "missing", name: "notes.txt", mime: "text/plain", byteLength: 4, snapshot: "missing" };
    const fine: LaunchChip = { kind: "bytes", itemId: "fine", name: "shot.png", mime: "image/png", byteLength: 9, snapshot: "ready" };
    const id = launch([missing, fine]);
    const actions = controller();
    render(() => <LaunchConversation launchId={id} controller={actions} onOpenProviders={vi.fn()} />);
    const problems = screen.getAllByTestId("launch-chip-problem");
    expect(problems.map((row) => row.getAttribute("data-item-id"))).toEqual(["missing"]);
    expect(screen.getByTestId("pending-send-attachment-chips").textContent).toContain("shot.png");
    fireEvent.click(screen.getByTestId("launch-chip-remove"));
    expect(actions.removeChip).toHaveBeenCalledWith(id, "missing");
  });

  it("edits the unsent idea and stops it", () => {
    const id = launch();
    const actions = controller();
    render(() => <LaunchConversation launchId={id} controller={actions} onOpenProviders={vi.fn()} />);
    fireEvent.click(screen.getByTestId("launch-edit"));
    fireEvent.input(screen.getByTestId("launch-edit-input"), { target: { value: "Build a chess game instead" } });
    fireEvent.click(screen.getByTestId("launch-edit-save"));
    expect(actions.edit).toHaveBeenCalledWith(id, "Build a chess game instead");
    fireEvent.click(screen.getByTestId("launch-stop"));
    expect(actions.stop).toHaveBeenCalledWith(id);
  });

  it("shows a proposed edit in place of the original while its send is uncertain", () => {
    const id = launch();
    updateLaunch(id, (r) => ({ ...r, sendAttempted: true, proposedText: "Proposed wording" }));
    render(() => <LaunchConversation launchId={id} controller={controller()} onOpenProviders={vi.fn()} />);
    expect(screen.getByTestId("pending-send-bubble").textContent).toContain("Proposed wording");
  });
});
