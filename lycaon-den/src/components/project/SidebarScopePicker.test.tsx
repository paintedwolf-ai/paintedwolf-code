import { stubClient } from "../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SidebarScopePicker } from "./SidebarScopePicker.tsx";
import {
  getDeletedLines,
  getMarkMyEdits,
  getSidebarScope,
  isComparisonOff,
  resetFilesStagePaneForTests,
  setComparisonOff,
  setSidebarScope,
} from "../../files/review/review-pane.ts";
import {
  resetScopeResolutionForTests,
  resolveScope,
} from "../../files/tree/scope-resolution.ts";
import { resetWalkForTests } from "../../files/walk/walk-store.ts";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { workingFileFixture } from "../../files/review/review-lens-fixtures.ts";
import {
  requestedFirstTimeTips,
  resetFirstTimeTipRequestsForTests,
} from "../../first-time-tips/first-time-tips-service.ts";
import { resetFirstTimeTipsPrefsForTests } from "../../settings/system/first-time-tips-prefs.ts";

function mockClient(overrides: Partial<LycaonClient> = {}): LycaonClient {
  return stubClient({
    listProjectSourcePins: vi.fn().mockResolvedValue({ pins: [] }),
    createProjectSourcePin: vi.fn().mockResolvedValue({
      id: "pin-new",
      project_id: "p1",
      label: "Before the big refactor",
      created_ts: "2026-07-30T00:00:00Z",
    }),
    deleteProjectSourcePin: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  });
}

// Seed addressable effects for the mark projection.
async function seedScope(
  files: Array<{ rootId: string; path: string }>,
): Promise<void> {
  const client = mockClient({
    listProjectSourceWalk: vi.fn().mockResolvedValue({
      baseline: "presentation",
      files: files.map((item) => ({
        ...workingFileFixture(item.path),
        root_id: item.rootId,
      })),
      git_changes: [],
      commands: [],
      turns: [],
      commit_available: true,
    }),
  });
  await resolveScope("p1", client, null);
}

// current_turn is the source-ledger ordinal.
function mockAppStore(
  currentTurn = 1,
  session: { id?: string; title?: string } = {},
): AppStore {
  return {
    state: {
      currentSession: {
        id: session.id ?? "s1",
        title: session.title ?? "Focus",
        status: "idle",
        current_turn: currentTurn,
      },
      messages: [],
      workers: [],
    },
  } as unknown as AppStore;
}

describe("SidebarScopePicker", () => {
  afterEach(() => {
    cleanup();
    resetFilesStagePaneForTests();
    resetScopeResolutionForTests();
    resetWalkForTests();
    resetFirstTimeTipRequestsForTests();
    resetFirstTimeTipsPrefsForTests(undefined, false);
  });

  it.each([false, true])("hydrates a restored pin label after delayed loading while comparison off is %s", async (off) => {
    setSidebarScope("p1", { kind: "pin", pinId: "pin-1" });
    setComparisonOff("p1", off);
    let finish!: (value: Awaited<ReturnType<LycaonClient["listProjectSourcePins"]>>) => void;
    const api = mockClient({
      listProjectSourcePins: vi.fn(() => new Promise<Awaited<ReturnType<LycaonClient["listProjectSourcePins"]>>>((resolve) => { finish = resolve; })),
    });
    render(() => <SidebarScopePicker projectId="p1" client={api} appStore={mockAppStore()} />);
    const trigger = screen.getByTestId("sidebar-scope-picker");
    expect(trigger.getAttribute("aria-label")).toBe(off ? "Scope: Off" : "Scope: Saved pin");
    finish({ pins: [{ id: "pin-1", project_id: "p1", label: "Before café refactor", created_at: "2026-09-12T00:00:00Z" }] });
    await waitFor(() => expect(getSidebarScope("p1")).toEqual({ kind: "pin", pinId: "pin-1", label: "Before café refactor" }));
    expect(trigger.getAttribute("aria-label")).toBe(off ? "Scope: Off" : "Scope: Before café refactor");
    expect(isComparisonOff("p1")).toBe(off);
  });

  it("introduces review scope when rendered and opens the menu on click", () => {
    resetFirstTimeTipsPrefsForTests({ enabled: true, dismissed: [] });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
      />
    ));
    const trigger = screen.getByTestId("sidebar-scope-picker");
    expect(requestedFirstTimeTips()).toContain("files-review-scope");

    fireEvent.click(trigger);
    expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy();
  });

  it("is present with no session activity and opens both modes' entries", async () => {
    await seedScope([{ rootId: "r1", path: "README.md" }]);
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
      />
    ));
    const btn = screen.getByTestId("sidebar-scope-picker");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(btn.getAttribute("aria-label")).toContain("New since you looked");
    expect(screen.getByTestId("sidebar-scope-picker-eye")).toBeTruthy();
    fireEvent.click(btn);
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    await waitFor(() =>
      expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy(),
    );
    const menu = screen.getByTestId("sidebar-scope-picker-menu");
    expect(menu.dataset.denAnchoredSurface).toBeTruthy();
    expect(
      menu.classList.contains("den-sidebar-scope-picker__menu"),
    ).toBe(true);
    expect(
      menu.querySelector(".den-sidebar-scope-picker__menu-list"),
    ).toBeTruthy();
    expect(screen.getByText("Review")).toBeTruthy();
    expect(screen.getByTestId("sidebar-scope-opt-new")).toBeTruthy();
    expect(screen.getByTestId("sidebar-scope-opt-commit")).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(btn.getAttribute("aria-expanded")).toBe("false");
  });

  it("offers five scope choices, with Off last and no ordered-review entry", async () => {
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    const menu = screen.getByTestId("sidebar-scope-picker-menu");
    const ids = [...menu.querySelectorAll("[data-testid^='sidebar-scope-opt-']")]
      .map((el) => el.getAttribute("data-testid"));
    expect(ids).toEqual([
      "sidebar-scope-opt-new",
      "sidebar-scope-opt-turn",
      "sidebar-scope-opt-chat",
      "sidebar-scope-opt-commit",
      "sidebar-scope-opt-off",
    ]);
  });

  it("keeps the comparison panel open while switching scopes", async () => {
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-turn"));
    expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy();
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-new"));
    expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy();
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });
  });

  it("saves and selects a snapshot in one click", async () => {
    const client = mockClient();
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-pin-current"));
    await waitFor(() =>
      expect(client.createProjectSourcePin).toHaveBeenCalledWith("p1", {
        label: expect.stringMatching(/^Snapshot /),
      }),
    );
    expect(getSidebarScope("p1")).toEqual({
      kind: "pin",
      pinId: "pin-new",
      label: "Before the big refactor",
    });
    expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy();
  });

  it("explains when a snapshot is waiting on repository inventory", async () => {
    const client = mockClient({
      createProjectSourcePin: vi.fn().mockRejectedValue(
        new LycaonApiError(
          "source inventory is still being prepared",
          409,
          "source_inventory_pending",
        ),
      ),
    });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-pin-current"));
    expect(
      await screen.findByText(
        "Repository history is still being indexed. Try again shortly.",
      ),
    ).toBeTruthy();
  });

  it("removes a saved snapshot and falls back from its active comparison", async () => {
    const client = mockClient({
      listProjectSourcePins: vi.fn().mockResolvedValue({
        pins: [
          {
            id: "pin-1",
            project_id: "p1",
            label: "Before the refactor",
            created_ts: "2026-07-30T00:00:00Z",
          },
        ],
      }),
    });
    setSidebarScope("p1", {
      kind: "pin",
      pinId: "pin-1",
      label: "Before the refactor",
    });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    const remove = await screen.findByTestId(
      "sidebar-scope-remove-pin-pin-1",
    );
    expect(remove.getAttribute("aria-label")).toContain("Before the refactor");
    fireEvent.click(remove);

    await waitFor(() =>
      expect(client.deleteProjectSourcePin).toHaveBeenCalledWith(
        "p1",
        "pin-1",
      ),
    );
    await waitFor(() =>
      expect(
        screen.queryByTestId("sidebar-scope-opt-pin-pin-1"),
      ).toBeNull(),
    );
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });
    expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy();
  });

  it("takes a pin out of the list before the host confirms and restores it if refused", async () => {
    let refuse!: (error: Error) => void;
    const client = mockClient({
      listProjectSourcePins: vi.fn().mockResolvedValue({
        pins: [
          {
            id: "pin-1",
            project_id: "p1",
            label: "Before the refactor",
            created_ts: "2026-07-30T00:00:00Z",
          },
        ],
      }),
      deleteProjectSourcePin: vi.fn(
        () => new Promise<void>((_resolve, fail) => { refuse = fail; }),
      ),
    });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(await screen.findByTestId("sidebar-scope-remove-pin-pin-1"));

    expect(screen.queryByTestId("sidebar-scope-opt-pin-pin-1")).toBeNull();

    refuse(new Error("offline"));
    expect(await screen.findByTestId("sidebar-scope-opt-pin-pin-1")).toBeTruthy();
    expect(screen.getByText("Couldn’t remove this pin. Try again.")).toBeTruthy();
  });

  it("loads retained pins one page at a time", async () => {
    setSidebarScope("p1", { kind: "pin", pinId: "pin-older" });
    const first = {
      id: "pin-newer",
      project_id: "p1",
      label: "Newer pin",
      created_ts: "2026-07-31T00:00:00Z",
    };
    const older = {
      id: "pin-older",
      project_id: "p1",
      label: "Older pin",
      created_ts: "2026-07-30T00:00:00Z",
    };
    const list = vi.fn().mockImplementation(
      async (_projectId: string, opts?: { cursor?: string }) =>
        opts?.cursor === "next" ? { pins: [older] } : { pins: [first], next_cursor: "next" },
    );
    const api = mockClient({ listProjectSourcePins: list });
    render(() => (
      <SidebarScopePicker projectId="p1" client={api} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    await screen.findByTestId("sidebar-scope-opt-pin-pin-newer");
    expect(screen.queryByTestId("sidebar-scope-opt-pin-pin-older")).toBeNull();

    fireEvent.click(screen.getByTestId("sidebar-scope-load-older-pins"));
    await screen.findByTestId("sidebar-scope-opt-pin-pin-older");
    expect(screen.getByTestId("sidebar-scope-picker").getAttribute("aria-label")).toBe("Scope: Older pin");
    expect(list).toHaveBeenLastCalledWith("p1", { cursor: "next" });
    expect(screen.queryByTestId("sidebar-scope-load-older-pins")).toBeNull();
  });

  it("turns the comparison off and back on from the same menu", async () => {
    setSidebarScope("p1", { kind: "commit" });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
      />
    ));
    const btn = screen.getByTestId("sidebar-scope-picker");
    fireEvent.click(btn);
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-off"));

    expect(isComparisonOff("p1")).toBe(true);
    expect(btn.getAttribute("aria-label")).toBe("Scope: Off");
    expect(
      screen.getByTestId("sidebar-scope-opt-off").getAttribute("aria-pressed"),
    ).toBe("true");
    // Off retains the stored scope without selecting it.
    expect(getSidebarScope("p1")).toEqual({ kind: "commit" });
    expect(
      screen
        .getByTestId("sidebar-scope-opt-commit")
        .getAttribute("aria-pressed"),
    ).toBe("false");

    fireEvent.click(screen.getByTestId("sidebar-scope-opt-new"));
    expect(isComparisonOff("p1")).toBe(false);
    expect(btn.getAttribute("aria-label")).toBe("Scope: New since you looked");
    expect(
      screen.getByTestId("sidebar-scope-opt-off").getAttribute("aria-pressed"),
    ).toBe("false");
  });

  it("leaves the person's edits unmarked until they check Mark my edits", () => {
    setSidebarScope("p1", { kind: "new" });
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    const row = screen.getByTestId("sidebar-scope-mark-my-edits");
    const control = screen.getByTestId("sidebar-scope-mark-my-edits-control") as HTMLInputElement;
    expect(row.textContent?.trim()).toBe("Mark my edits");
    expect(control.checked).toBe(false);

    fireEvent.click(control);
    expect(getMarkMyEdits("p1")).toBe(true);
    expect(control.checked).toBe(true);
  });

  it("marks every change under the commit comparison", () => {
    setSidebarScope("p1", { kind: "commit" });
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    const control = screen.getByTestId("sidebar-scope-mark-my-edits-control") as HTMLInputElement;
    expect(control.disabled).toBe(true);
    expect(control.checked).toBe(true);
    expect(screen.getByTestId("sidebar-scope-mark-my-edits").dataset.tip).toBe(
      "Git doesn’t record who made a change, so every change is marked.",
    );
  });

  it("folds deleted lines by default and switches them in place", () => {
    setSidebarScope("p1", { kind: "new" });
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    const folded = screen.getByTestId("sidebar-scope-deleted-lines-folded");
    const inPlace = screen.getByTestId("sidebar-scope-deleted-lines-inplace");
    expect(folded.getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(inPlace);
    expect(getDeletedLines("p1")).toBe("inplace");
    expect(inPlace.getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByTestId("sidebar-scope-deleted-lines").textContent).toContain(
      "Shown above the lines that replaced them.",
    );
  });

  it("leaves the turn number to the host, which resolves the chat's current turn", async () => {
    setSidebarScope("p1", { kind: "new" });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore(7)}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-turn"));
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });
  });

  it("keeps ledger comparisons available while disabling Git when unavailable", async () => {
    setSidebarScope("p1", { kind: "new" });
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore(0)}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    const turn = screen.getByTestId("sidebar-scope-opt-turn");
    const commit = screen.getByTestId("sidebar-scope-opt-commit");
    expect(turn.getAttribute("disabled")).toBeNull();
    expect(commit.getAttribute("disabled")).toBe("");

    fireEvent.click(turn);
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });

    fireEvent.click(commit);
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });
  });

  it("offers no session list and names the selected chat as the subject", async () => {
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore(1, { title: "Fix auth flow" })}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    expect(screen.queryByTestId("sidebar-scope-opt-session-s1")).toBeNull();
    expect(screen.queryByText("Sessions")).toBeNull();
    expect(
      screen.getByTestId("sidebar-scope-picker-subject").textContent,
    ).toContain("Fix auth flow");
  });

  it("scopes the whole chat to the selection, with no id to pick", async () => {
    render(() => (
      <SidebarScopePicker
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore(3, { id: "s9", title: "Fix auth flow" })}
      />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-chat"));
    expect(getSidebarScope("p1")).toEqual({ kind: "session" });
    expect(
      screen.getByTestId("sidebar-scope-picker").getAttribute("aria-label"),
    ).toBe("Scope: Everything in this chat");
  });

  it("keeps a chat choice when another chat is selected, since it names none", async () => {
    const [session, setSession] = createSignal({ id: "s1", title: "Focus" });
    const store = {
      get state() {
        return {
          currentSession: {
            id: session().id,
            title: session().title,
            status: "idle",
            current_turn: 4,
          },
          messages: [],
          workers: [],
        };
      },
    } as unknown as AppStore;
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={store} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-turn"));

    setSession({ id: "s2", title: "Refactor store" });
    await waitFor(() =>
      expect(screen.getByTestId("sidebar-scope-picker-subject").textContent).toContain("Refactor store"),
    );
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });
  });

  it("disables chat choices when no chat is selected", async () => {
    const store = { state: { currentSession: null, messages: [], workers: [] } } as unknown as AppStore;
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={store} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    expect(screen.getByTestId("sidebar-scope-opt-turn").getAttribute("disabled")).toBe("");
    expect(screen.getByTestId("sidebar-scope-opt-chat").getAttribute("disabled")).toBe("");
  });
});

describe("the scope picker", () => {
  afterEach(() => {
    cleanup();
    resetFilesStagePaneForTests();
    resetScopeResolutionForTests();
    resetWalkForTests();
  });

  it("keeps Walk out of the comparison menu", async () => {
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    await waitFor(() => expect(screen.getByTestId("sidebar-scope-picker-menu")).toBeTruthy());
    expect(screen.queryByTestId("sidebar-scope-opt-step")).toBeNull();
    expect(screen.getByTestId("sidebar-scope-opt-new")).toBeTruthy();
    expect(screen.getByTestId("sidebar-scope-opt-off")).toBeTruthy();
  });


  it('renders a load error, not "No pins yet", when the pin read fails', async () => {
    // A read failure does not establish an empty pin list.
    const api = mockClient({
      listProjectSourcePins: vi.fn().mockRejectedValue(new Error("sidecar restarting")),
    });
    render(() => (
      <SidebarScopePicker projectId="p1" client={api} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    await screen.findByTestId("sidebar-scope-pins-load-error");
    expect(screen.queryByText("No pins yet")).toBeNull();
  });

  it('says "No pins yet" only after a load proved the project has none', async () => {
    render(() => (
      <SidebarScopePicker projectId="p1" client={mockClient()} appStore={mockAppStore()} />
    ));
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    await waitFor(() => expect(screen.getByText("No pins yet")).toBeTruthy());
    expect(screen.queryByTestId("sidebar-scope-pins-load-error")).toBeNull();
  });
});
