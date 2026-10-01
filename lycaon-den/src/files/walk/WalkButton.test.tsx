import { stubClient } from "../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { WalkButton } from "./WalkButton.tsx";
import { resetWalkForTests, walkState } from "./walk-store.ts";

function client(): LycaonClient {
  return stubClient({
    listProjectSourceWalk: vi.fn().mockResolvedValue({
      files: [], commit_available: true, git_changes: [], commands: [], turns: [],
    }),
    getProjectSourceComparison: vi.fn(),
  });
}

function store(sessionId = "s1", turn = 3): AppStore {
  return { state: { currentSession: {
    id: sessionId, title: "Current chat", status: "idle", current_turn: turn,
  } } } as AppStore;
}

afterEach(() => { cleanup(); resetWalkForTests(); });

describe("WalkButton", () => {
  it("starts the selected chat and closes the active Walk", async () => {
    const api = client();
    render(() => <WalkButton projectId="p1" client={api} appStore={store()} />);
    fireEvent.click(screen.getByTestId("walk-button"));
    await waitFor(() => expect(walkState("p1").status).toBe("ready"));
    expect(walkState("p1").sessionId).toBe("s1");
    expect(api.listProjectSourceWalk).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByTestId("walk-button"));
    expect(walkState("p1").active).toBe(false);
  });

  it("works before the selected chat has a completed turn", async () => {
    const api = client();
    render(() => <WalkButton projectId="p1" client={api} appStore={store("s1", 0)} />);
    fireEvent.click(screen.getByTestId("walk-button"));
    await waitFor(() => expect(walkState("p1").status).toBe("ready"));
    expect(walkState("p1").sessionId).toBe("s1");
  });

  it("keeps the opened chat when chat selection changes", async () => {
    const [sessionId, setSessionId] = createSignal("s1");
    const appStore = { get state() { return { currentSession: {
      id: sessionId(), title: "Current chat", status: "idle", current_turn: 3,
    } }; } } as AppStore;
    const api = client();
    render(() => <WalkButton projectId="p1" client={api} appStore={appStore} />);
    fireEvent.click(screen.getByTestId("walk-button"));
    await waitFor(() => expect(walkState("p1").status).toBe("ready"));
    setSessionId("s2");
    expect(walkState("p1").sessionId).toBe("s1");
  });

  it("is unavailable without an open chat", () => {
    const api = client();
    render(() => <WalkButton projectId="p1" client={api} appStore={{ state: {} } as AppStore} />);
    expect(screen.getByTestId("walk-button").hasAttribute("disabled")).toBe(true);
  });
});
