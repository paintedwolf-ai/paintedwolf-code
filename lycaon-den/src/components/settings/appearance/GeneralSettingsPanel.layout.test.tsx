import "../../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GeneralSettingsPanel } from "./GeneralSettingsPanel.tsx";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../../store/app-state-snapshot.ts";
import { syncLayoutFromSnapshot } from "../../../shell/layout-store.ts";

vi.mock("../../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    loadSharedAppState: async () => actual.getAppStateSnapshot(),
  };
});

async function openDisplayTab() {
  render(() => <GeneralSettingsPanel initialTab="display" version="0.0.0-test" />);
  await screen.findByTestId("display-layout-group");
}

/** Opens the select and picks the option carrying `value`. */
async function choose(testId: string, value: string) {
  fireEvent.click(screen.getByTestId(testId));
  const option = await waitFor(() => {
    const found = document.querySelector(`[role="option"][data-value="${value}"]`);
    if (!found) throw new Error(`no option for ${value}`);
    return found;
  });
  fireEvent.click(option);
}

const survivor = () => getAppStateSnapshot().layout?.narrowSurvivor;

describe("GeneralSettingsPanel layout group", () => {
  afterEach(() => {
    cleanup();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncLayoutFromSnapshot();
  });

  it("saves chat position and mirroring as independent choices", async () => {
    await openDisplayTab();
    fireEvent.click(screen.getByTestId("display-chat-first"));
    await waitFor(() => expect(getAppStateSnapshot().layout?.splitOrder).toBe("chat-first"));
    fireEvent.click(screen.getByTestId("display-workspace-mirrored"));
    await waitFor(() => expect(getAppStateSnapshot().layout?.workspaceOrientation).toBe("mirrored"));
    expect(getAppStateSnapshot().layout?.splitOrder).toBe("chat-first");
    fireEvent.click(screen.getByTestId("display-context-first"));
    await waitFor(() => expect(getAppStateSnapshot().layout?.splitOrder).toBe("context-first"));
    expect(getAppStateSnapshot().layout?.workspaceOrientation).toBe("mirrored");
  });

  it("names the column the launch layout implies until one is pinned", async () => {
    await openDisplayTab();
    const row = screen.getByTestId("display-narrow-survivor");
    expect(row.textContent).toBe("Match open at launch (conversation)");

    await choose("display-startup-companion", "files");
    await waitFor(() =>
      expect(screen.getByTestId("display-narrow-survivor").textContent).toBe(
        "Match open at launch (context view)",
      ),
    );
    // An absent override follows the launch layout.
    expect(survivor()).toBeUndefined();
  });

  it("pins a survivor against the launch layout, and gives it back", async () => {
    await openDisplayTab();
    await choose("display-startup-companion", "files");

    await choose("display-narrow-survivor", "conversation");
    await waitFor(() => expect(survivor()).toBe("conversation"));
    expect(screen.getByTestId("display-narrow-survivor").textContent).toBe(
      "Keep the conversation",
    );

    await choose("display-narrow-survivor", "stage");
    await waitFor(() => expect(survivor()).toBe("stage"));

    await choose("display-narrow-survivor", "");
    await waitFor(() => expect(survivor()).toBeUndefined());
    expect(screen.getByTestId("display-narrow-survivor").textContent).toBe(
      "Match open at launch (context view)",
    );
  });
});
