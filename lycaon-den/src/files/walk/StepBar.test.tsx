import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { stubFilesClient as stubClient } from "../../test/source-client-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { WalkChromeFixture } from "./walk-chrome-fixture.tsx";
import { walkClientFixture, walkEffectFixture, walkResponseFixture } from "./walk-fixtures.ts";
import {
  enterWalk,
  isWalking,
  refreshWalk,
  resetWalkForTests,
  setWalkAt,
  walkState,
  walkToLatest,
} from "./walk-store.ts";

import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { resetAppStateSnapshotForTests, getAppStateSnapshot } from "../../store/app-state-snapshot.ts";

vi.mock("../../platform/persistence/app-state.ts", () => ({
  patchAppState: vi.fn(async (_patch, fallback) => fallback),
}));

const PID = "p1";
const SID = "s1";

function changes() {
  const row = (id: string, path: string, toolCallId: string, ordinal: number) => sourceEffectFixture({
    id,
    project_id: PID,
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project" as const,
    root_id: "r1",
    path,
    op: "write" as const,
    entry_kind: "file",
    origin: "agent" as const,
    session_id: SID,
    turn: 1,
    ordinal,
    observed_at: "2026-08-09T10:02:00Z",
    tool_call_id: toolCallId,
    cause: "tool",
    tool_name: "edit",
    capture_quality: "exact" as const,
  });
  const file = (path: string, id: string, toolCallId: string, ordinal: number) => ({
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    changed_since_presented: false,
    tip: { state: "content", sha256: "sha" },
    unpresented_agent_effects: 1,
    effects: [row(id, path, toolCallId, ordinal)],
  });
  return {
    files: [
      file("a.ts", "c0", "t1", 1),
      file("b.ts", "c1", "t2", 2),
      file("c.ts", "c2", "t2", 3),
      file("d.ts", "c3", "t2", 4),
      file("e.ts", "c4", "t2", 5),
      file("f.ts", "c5", "t2", 6),
    ],
    commit_available: false,
    git_changes: [], commands: [], turns: [],
  };
}

function client(overrides: Record<string, unknown> = {}) {
  return stubClient({
    readComparison: vi.fn(async () => ({
      in_range: true,
      before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
      after: { state: "content", size_bytes: 0, availability: "available", content: "" },
      location_changed: false,
    })),
    listProjectSourceWalk: vi.fn(async () => changes()),
    listProjectSourcePins: vi.fn(async () => ({ pins: [] })),
    ...overrides,
  });
}

beforeEach(() => {
  resetWalkForTests();
  resetEditorPrefsForTests();
  resetAppStateSnapshotForTests();
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); registerNoticePublisher(null); });

async function mounted(c: LycaonClient = client()) {
  const view = render(() => <WalkChromeFixture projectId={PID} />);
  await enterWalk(PID, c, SID);
  await waitFor(() => expect(screen.queryByTestId("step-bar-rail")).toBeTruthy());
  await waitFor(() => expect(screen.getByTestId("walk-transport").dataset.boot).toBe("ready"));
  return view;
}

describe("StepBar", () => {
  it("opens on the earliest effect", async () => {
    await mounted();
    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 6");
  });

  it("renders one dot per effect", async () => {
    const { container } = await mounted();
    expect(container.querySelectorAll(".den-step-bar__dot")).toHaveLength(6);
  });

  it("shows useful deterministic details when a dot is hovered", async () => {
    await mounted();
    const dot = screen.getAllByTestId("step-bar-dot")[0]!;
    fireEvent.mouseEnter(dot);

    const tooltip = await screen.findByTestId("step-bar-tooltip");
    expect(tooltip.classList.contains("den-status-popover")).toBe(true);
    expect(tooltip.querySelector(".den-status-mark")).toBeTruthy();
    expect(tooltip.textContent).toContain("Step 1 of 6");
    expect(tooltip.textContent).toContain("Modified");
    expect(tooltip.textContent).toContain("a.ts");
    expect(tooltip.textContent).toContain("Agent");
    expect(tooltip.textContent).toContain("Turn 1");
    expect(tooltip.textContent).toContain("edit");

    fireEvent.mouseLeave(dot);
    fireEvent.mouseEnter(tooltip);
    await new Promise((resolve) => setTimeout(resolve, 180));
    expect(screen.getByTestId("step-bar-tooltip")).toBe(tooltip);

    fireEvent.mouseLeave(tooltip);
    await waitFor(() => expect(screen.queryByTestId("step-bar-tooltip")).toBeNull());
  });

  it("marks a command's dot and says what it observed on hover", async () => {
    const base = changes();
    const withCommand = {
      ...base,
      files: [
        ...base.files,
        {
          file_id: "file-Cargo.lock",
          root_id: "r1",
          path: "Cargo.lock",
          changed_since_presented: false,
          tip: { state: "content", sha256: "sha-l" },
          unpresented_agent_effects: 0,
          effects: [sourceEffectFixture({
            id: "cw1",
            project_id: PID,
            operation_id: "operation-cw1",
            file_id: "file-Cargo.lock",
            after_version_id: "version-cw1",
            workspace_kind: "project" as const,
            root_id: "r1",
            path: "Cargo.lock",
            op: "create",
            entry_kind: "file",
            origin: "external",
            session_id: "s1",
            turn: 1,
            ordinal: 9,
            observed_at: "2026-08-09T10:04:00Z",
            cause: "command_window",
            capture_quality: "reconciled" as const,
            command_id: "w-build",
          })],
        },
      ],
      commands: [{
        id: "w-build",
        session_id: "s1",
        turn: 1,
        tool_call_id: "t-build",
        tool_name: "command",
        command_line: "cargo build",
        state: "ended",
        admission_mode: "scope_bounded",
        ordinal: 8,
        started_at: "2026-08-09T10:03:30Z",
        ended_at: "2026-08-09T10:04:00Z",
      }],
    };
    const c = client({ listProjectSourceWalk: vi.fn(async () => withCommand) });
    const { container } = await mounted(c);

    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 7");
    const dots = container.querySelectorAll(".den-step-bar__dot--command");
    expect(dots).toHaveLength(1);
    expect(dots[0]!.getAttribute("aria-label")).toContain("Command");
    expect(dots[0]!.getAttribute("aria-label")).toContain("cargo build");
    expect(dots[0]!.getAttribute("aria-label")).toContain("1 file");
    expect(dots[0]!.querySelector("rect.den-step-marker__body")).toBeTruthy();

    fireEvent.mouseEnter(dots[0]!);
    const tooltip = await screen.findByTestId("step-bar-tooltip");
    expect(tooltip.textContent).toContain("cargo build");
    expect(tooltip.textContent).toContain("1 file observed while it ran");
    expect(tooltip.textContent).toContain("Command");
  });

  it("marks a git movement's dot and describes it on hover", async () => {
    const base = changes();
    const withGit = {
      ...base,
      files: [
        ...base.files,
        {
          file_id: "file-swapped.ts",
          root_id: "r1",
          path: "swapped.ts",
          changed_since_presented: false,
          tip: { state: "content", sha256: "sha-g" },
          unpresented_agent_effects: 0,
          effects: [sourceEffectFixture({
            id: "cg1",
            project_id: PID,
            operation_id: "operation-cg1",
            file_id: "file-swapped.ts",
            after_version_id: "version-cg1",
            workspace_kind: "project" as const,
            root_id: "r1",
            path: "swapped.ts",
            op: "write",
            entry_kind: "file",
            origin: "external",
            turn: 0,
            ordinal: 8,
            observed_at: "2026-08-09T10:03:00Z",
            cause: "filesystem_reconcile",
            capture_quality: "reconciled" as const,
            git_change_id: "t-git",
          })],
        },
      ],
      git_changes: [{
        session_id: "", turn: 0, tool_call_id: "", tool_name: "",
        id: "t-git",
        root_id: "r1",
        kind: "checkout",
        from_ref: "main",
        to_ref: "feature-x",
        detail: "moving from main to feature-x",
        ordinal: 7,
        observed_at: "2026-08-09T10:03:00Z",
      }],
    };
    const c = client({ listProjectSourceWalk: vi.fn(async () => withGit) });
    const { container } = await mounted(c);

    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 7");
    const gitDots = container.querySelectorAll(".den-step-bar__dot--git");
    expect(gitDots).toHaveLength(1);
    expect(gitDots[0]!.getAttribute("aria-label")).toContain("Git checkout");
    expect(gitDots[0]!.getAttribute("aria-label")).toContain("main → feature-x");
    expect(gitDots[0]!.getAttribute("aria-label")).toContain("1 observed file");
    expect(gitDots[0]!.querySelector(".den-step-marker--hollow circle.den-step-marker__body")).toBeTruthy();
    expect(container.querySelector(".den-step-bar__dot--write .den-step-marker--hollow")).toBeNull();

    fireEvent.mouseEnter(gitDots[0]!);
    const tooltip = await screen.findByTestId("step-bar-tooltip");
    expect(tooltip.textContent).toContain("Git checkout");
    expect(tooltip.textContent).toContain("main → feature-x");
    expect(tooltip.textContent).toContain("1 observed file");
    expect(tooltip.textContent).toContain("moving from main to feature-x");
  });

  it("marks an outside run's dot and counts its files on hover", async () => {
    const base = changes();
    const outsideFile = (path: string, id: string, ordinal: number) => ({
      file_id: `file-${path}`,
      root_id: "r1",
      path,
      changed_since_presented: false,
      tip: { state: "content", sha256: `sha-${id}` },
      unpresented_agent_effects: 0,
      effects: [sourceEffectFixture({
        id,
        project_id: PID,
        operation_id: `operation-${id}`,
        file_id: `file-${path}`,
        after_version_id: `version-${id}`,
        workspace_kind: "project" as const,
        root_id: "r1",
        path,
        op: "write",
        entry_kind: "file",
        origin: "external",
        turn: 0,
        ordinal,
        observed_at: "2026-08-09T10:04:00Z",
        cause: "filesystem_reconcile",
        capture_quality: "reconciled" as const,
      })],
    });
    const withOutside = {
      ...base,
      files: [...base.files, outsideFile("gen/x.ts", "o1", 9), outsideFile("gen/y.ts", "o2", 10)],
    };
    const c = client({ listProjectSourceWalk: vi.fn(async () => withOutside) });
    const { container } = await mounted(c);

    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 7");
    const dots = container.querySelectorAll(".den-step-bar__dot--outside");
    expect(dots).toHaveLength(1);
    expect(dots[0]!.getAttribute("aria-label")).toContain("Outside the app");
    expect(dots[0]!.getAttribute("aria-label")).toContain("2 files");

    const body = dots[0]!.querySelector("circle.den-step-marker__body");
    expect(body?.getAttribute("stroke-dasharray")).toBeTruthy();

    fireEvent.mouseEnter(dots[0]!);
    const tooltip = await screen.findByTestId("step-bar-tooltip");
    expect(tooltip.querySelector(".den-step-tooltip__file")?.textContent).toBe("2 files");
    expect(tooltip.textContent).toContain("gen/x.ts");
    expect(tooltip.textContent).toContain("gen/y.ts");
    expect(tooltip.textContent?.split("Outside the app")).toHaveLength(2);
  });

  it("names the one file an outside run saved repeatedly", async () => {
    const base = changes();
    const save = (id: string, ordinal: number, observed_at: string) => sourceEffectFixture({
      id,
      project_id: PID,
      operation_id: `operation-${id}`,
      file_id: "file-gen/x.ts",
      after_version_id: `version-${id}`,
      workspace_kind: "project" as const,
      root_id: "r1",
      path: "gen/x.ts",
      op: "write",
      entry_kind: "file",
      origin: "external",
      turn: 0,
      ordinal,
      observed_at,
      cause: "filesystem_reconcile",
      capture_quality: "reconciled" as const,
    });
    const withOutside = {
      ...base,
      files: [...base.files, {
        file_id: "file-gen/x.ts",
        root_id: "r1",
        path: "gen/x.ts",
        changed_since_presented: false,
        tip: { state: "content", sha256: "sha-x" },
        unpresented_agent_effects: 0,
        effects: [save("o1", 9, "2026-08-09T10:04:00Z"), save("o2", 10, "2026-08-09T10:05:00Z")],
      }],
    };
    const { container } = await mounted(client({ listProjectSourceWalk: vi.fn(async () => withOutside) }));

    fireEvent.mouseEnter(container.querySelector(".den-step-bar__dot--outside")!);
    const tooltip = await screen.findByTestId("step-bar-tooltip");
    expect(tooltip.querySelector(".den-step-tooltip__file")?.textContent).toBe("x.ts");
    expect(tooltip.textContent).toContain("gen/x.ts");
    expect(tooltip.textContent?.split("Outside the app")).toHaveLength(2);
  });

  it("steps forward", async () => {
    await mounted();
    const dots = screen.getAllByTestId("step-bar-dot");
    expect(dots[0]?.classList.contains("den-step-bar__dot--current")).toBe(true);
    fireEvent.click(screen.getByTestId("walk-transport-next"));
    await waitFor(() =>
      expect(screen.getByTestId("step-bar-read").textContent).toContain("2 of 6"),
    );
    expect(dots[0]?.classList.contains("den-step-bar__dot--current")).toBe(false);
    expect(dots[1]?.classList.contains("den-step-bar__dot--current")).toBe(true);
    expect(dots[0]?.classList.contains("den-step-bar__dot--past")).toBe(true);
  });

  it("holds the playhead on the settled step until the requested comparison arrives", async () => {
    let arrive!: () => void;
    const comparison = {
      in_range: true,
      before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
      after: { state: "content", size_bytes: 0, availability: "available", content: "" },
      location_changed: false,
    };
    const gated = new Promise<void>((resolve) => { arrive = resolve; });
    let holding = false;
    const c = client({
      readComparison: vi.fn(async () => {
        if (holding) await gated;
        return comparison;
      }),
    });
    await mounted(c);
    holding = true;
    const dot = () => screen.getAllByTestId("step-bar-dot");

    fireEvent.keyDown(dot()[0]!, { key: "ArrowRight" });
    fireEvent.keyDown(dot()[1]!, { key: "ArrowRight" });

    expect(walkState(PID).targetAt).toBe(2);
    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 6");
    expect(dot()[0]?.classList.contains("den-step-bar__dot--current")).toBe(true);

    arrive();
    await waitFor(() =>
      expect(screen.getByTestId("step-bar-read").textContent).toContain("3 of 6"),
    );
    expect(dot()[2]?.classList.contains("den-step-bar__dot--current")).toBe(true);
  });

  it("disables the transport at each end", async () => {
    await mounted();
    const disabled = (id: string) =>
      (screen.getByTestId(id) as HTMLButtonElement).disabled;
    expect(disabled("walk-transport-prev")).toBe(true);
    expect(disabled("walk-transport-next")).toBe(false);
    fireEvent.click(screen.getByTestId("walk-transport-last"));
    await waitFor(() => expect(disabled("walk-transport-next")).toBe(true));
    expect(disabled("walk-transport-prev")).toBe(false);
  });

  it("walks with the arrow keys and leaves on Escape", async () => {
    await mounted();
    const dot = screen.getAllByTestId("step-bar-dot")[0]!;
    fireEvent.keyDown(dot, { key: "Home" });
    await waitFor(() => expect(walkState(PID).at).toBe(0));
    fireEvent.keyDown(dot, { key: "ArrowRight" });
    await waitFor(() => expect(walkState(PID).at).toBe(1));
    fireEvent.keyDown(screen.getAllByTestId("step-bar-dot")[1]!, { key: "End" });
    await waitFor(() => expect(walkState(PID).at).toBe(5));
    fireEvent.keyDown(screen.getAllByTestId("step-bar-dot")[5]!, { key: "Escape" });
    await waitFor(() => expect(isWalking(PID)).toBe(false));
  });

  it("uses a roving accessible dot for the playhead", async () => {
    await mounted();
    const dots = screen.getAllByTestId("step-bar-dot") as HTMLButtonElement[];
    expect(dots[0]?.getAttribute("aria-current")).toBe("step");
    expect(dots[0]?.tabIndex).toBe(0);
    expect(dots[0]?.getAttribute("aria-label")).toContain("Step 1 of 6");
    expect(dots.slice(1).every((dot) => dot.tabIndex === -1)).toBe(true);
  });

  it("docks on request and returns to free positioning", async () => {
    await mounted();
    const transport = screen.getByTestId("walk-transport");
    const grip = screen.getByTestId("walk-transport-grip");
    expect(transport.classList.contains("den-walk-transport--docked")).toBe(false);
    fireEvent.keyDown(grip, { key: "End" });
    expect(transport.classList.contains("den-walk-transport--docked")).toBe(true);
    fireEvent.keyDown(grip, { key: "ArrowUp" });
    expect(transport.classList.contains("den-walk-transport--docked")).toBe(false);
    expect(transport.classList.contains("den-walk-transport--positioned")).toBe(true);
    fireEvent.keyDown(grip, { key: "Home" });
    expect(transport.classList.contains("den-walk-transport--positioned")).toBe(false);
  });

  it("restores a docked location after remounting", async () => {
    const view = await mounted();
    fireEvent.keyDown(screen.getByTestId("walk-transport-grip"), { key: "End" });
    expect(getAppStateSnapshot().editor?.walkControlsLocation).toEqual({ placement: "docked" });
    view.unmount();
    render(() => <WalkChromeFixture projectId={PID} />);
    await waitFor(() => expect(screen.getByTestId("walk-transport").classList.contains("den-walk-transport--docked")).toBe(true));
  });

  it("uses the configured default when no location has been saved", async () => {
    resetEditorPrefsForTests({ walkControlsDefault: "docked" });
    await mounted();
    expect(screen.getByTestId("walk-transport").classList.contains("den-walk-transport--docked")).toBe(true);
  });

  it("closes the walk from the floating transport", async () => {
    await mounted();
    await waitFor(() => expect(screen.getByTestId("walk-transport").dataset.boot).toBe("ready"));
    fireEvent.click(screen.getByTestId("walk-transport-close"));
    expect(isWalking(PID)).toBe(false);
    expect(screen.queryByTestId("walk-transport")).toBeNull();
  });

  it("can be moved with the keyboard and reset to its default position", async () => {
    await mounted();
    const transport = screen.getByTestId("walk-transport");
    const grip = screen.getByTestId("walk-transport-grip");
    expect(transport.classList.contains("den-walk-transport--positioned")).toBe(
      false,
    );
    fireEvent.keyDown(grip, { key: "ArrowRight" });
    expect(transport.classList.contains("den-walk-transport--positioned")).toBe(
      true,
    );
    fireEvent.keyDown(grip, { key: "Home" });
    expect(transport.classList.contains("den-walk-transport--positioned")).toBe(
      false,
    );
    expect(grip.hasAttribute("data-tip")).toBe(false);
  });

  it("does not render a loading fragment inside the rail", async () => {
    let release!: (v: unknown) => void;
    const slow = client({
      listProjectSourceWalk: vi.fn(() => new Promise((r) => (release = r))),
    });
    render(() => <WalkChromeFixture projectId={PID} />);
    const pending = enterWalk(PID, slow, SID);
    expect(screen.queryByTestId("step-bar-loading")).toBeNull();
    expect(screen.queryByTestId("step-bar-read")).toBeNull();
    release(changes());
    await pending;
  });

  it("reports a run that could not be loaded as a notice, not in the bar", async () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    const broken = client({
      listProjectSourceWalk: vi.fn(async () => {
        throw new Error("offline");
      }),
    });
    render(() => <WalkChromeFixture projectId={PID} />);
    await enterWalk(PID, broken, SID);
    const rows = selectProjectNoticeGroups(store.index()).find((group) => group.projectId === PID)?.notices ?? [];
    expect(rows).toMatchObject([{ code: "walk_unavailable", message: "offline" }]);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("step-bar-empty")).toBeNull();
  });

  it("says so when a run holds no file changes", async () => {
    const empty = client({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [],
        commit_available: false,
        git_changes: [], commands: [], turns: [],
      })),
    });
    render(() => <WalkChromeFixture projectId={PID} />);
    await enterWalk(PID, empty, SID);
    await waitFor(() =>
      expect(screen.getByTestId("step-bar-empty").textContent).toContain(
        "no file changes",
      ),
    );
  });

  it("follows the store when something else moves the playhead", async () => {
    await mounted();
    setWalkAt(PID, 0);
    await waitFor(() =>
      expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 6"),
    );
  });
});

function growingClient() {
  const steps = [
    { call: "t1", change: "c0", ord: 2 },
    { call: "t2", change: "c1", ord: 3 },
  ];
  return {
    append(call: string, change: string, ord: number) {
      steps.push({ call, change, ord });
    },
    client: stubClient({
      readComparison: vi.fn(async () => ({
        in_range: true,
        before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
        after: { state: "content", size_bytes: 0, availability: "available", content: "" },
        location_changed: false,
      })),
      listProjectSourceWalk: vi.fn(async () => ({
        files: steps.map((s) => ({
          file_id: `file-${s.call}.ts`,
          root_id: "r1",
          path: `${s.call}.ts`,
          changed_since_presented: false,
          unpresented_agent_effects: 1,
          tip: { state: "content", sha256: "sha" },
          effects: [
            sourceEffectFixture({
              id: s.change,
              project_id: PID,
              operation_id: `operation-${s.change}`,
              file_id: `file-${s.call}.ts`,
              after_version_id: `version-${s.change}`,
              workspace_kind: "project",
              root_id: "r1",
              path: `${s.call}.ts`,
              op: "write",
              entry_kind: "file",
              origin: "agent",
              turn: 1,
              ordinal: s.ord,
              observed_at: "2026-08-09T10:00:00Z",
              tool_call_id: s.call,
              cause: "tool",
              tool_name: "edit",
              capture_quality: "exact",
            }),
          ],
        })),
        commit_available: false,
        git_changes: [], commands: [], turns: [],
      })),
      listProjectSourcePins: vi.fn(async () => ({ pins: [] })),
    }),
  };
}

function dotLefts(container: HTMLElement): string[] {
  return [...container.querySelectorAll(".den-step-bar__dot")].map(
    (el) => (el as HTMLElement).style.left,
  );
}

function withRailWidth(px: number): () => void {
  const proto = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "clientWidth",
  );
  Object.defineProperty(HTMLElement.prototype, "clientWidth", {
    configurable: true,
    get: () => px,
  });
  return () => {
    if (proto) Object.defineProperty(HTMLElement.prototype, "clientWidth", proto);
    else delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientWidth;
  };
}

describe("a rail that grows during a live run", () => {
  it("keeps the refresh tooltip current across successive new chapters", async () => {
    const effects = [walkEffectFixture("a", 1, 1)];
    await mounted(walkClientFixture(() => walkResponseFixture(effects)));
    effects.push(walkEffectFixture("b", 2, 2));
    await refreshWalk(PID);
    expect(screen.getByTestId("walk-transport-refresh").getAttribute("aria-label")).toBe("Show 1 new step · 1 new turn");

    effects.push(walkEffectFixture("c", 3, 3));
    await refreshWalk(PID);
    expect(screen.getByTestId("walk-transport-refresh").getAttribute("aria-label")).toBe("Show 2 new steps · 2 new turns");

    setWalkAt(PID, 1);
    await waitFor(() => expect(screen.getByTestId("walk-transport-refresh").getAttribute("aria-label")).toBe("Show 1 new step · 1 new turn"));
  });

  it("reflows existing markers without replacing their DOM nodes when a step lands", async () => {
    const restore = withRailWidth(200);
    try {
      const run = growingClient();
      const { container } = await mounted(run.client);
      setWalkAt(PID, 0);
      const before = dotLefts(container);
      const heldDots = [...container.querySelectorAll(".den-step-bar__dot")];
      run.append("t3", "c2", 4);
      await refreshWalk(PID);
      await waitFor(() => expect(container.querySelectorAll(".den-step-bar__dot")).toHaveLength(3));
      expect(dotLefts(container).slice(0, before.length)).not.toEqual(before);
      expect([...container.querySelectorAll(".den-step-bar__dot")].slice(0, heldDots.length)).toEqual(heldDots);
    } finally { restore(); }
  });

  it("keeps the refresh control mounted as arrivals become available and are read", async () => {
    const run = growingClient();
    await mounted(run.client);
    setWalkAt(PID, 0);
    const refresh = screen.getByTestId("walk-transport-refresh");
    expect(refresh.hasAttribute("disabled")).toBe(true);
    expect(refresh.closest('[data-testid="walk-transport"]')).not.toBeNull();
    expect(refresh.querySelector("svg")).not.toBeNull();
    expect(refresh.textContent).toBe("");

    run.append("t3", "c2", 4);
    await refreshWalk(PID);

    const jump = await screen.findByTestId("walk-transport-refresh");
    expect(jump).toBe(refresh);
    expect(jump.getAttribute("aria-label")).toContain("1 new");
    expect(jump.hasAttribute("disabled")).toBe(false);
    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 3");

    fireEvent.click(jump);
    await waitFor(() =>
      expect(screen.getByTestId("step-bar-read").textContent).toContain("3 of 3"),
    );
    expect(screen.getByTestId("walk-transport-refresh")).toBe(jump);
    expect(jump.hasAttribute("disabled")).toBe(true);
  });

  it("offers new arrivals while the reader stays on the former last step", async () => {
    const run = growingClient();
    await mounted(run.client);
    walkToLatest(PID);
    await waitFor(() => expect(walkState(PID).at).toBe(1));

    run.append("t3", "c2", 4);
    await refreshWalk(PID);

    await waitFor(() =>
      expect(screen.getByTestId("step-bar-read").textContent).toContain("2 of 3"),
    );
    expect(screen.getByTestId("walk-transport-refresh").getAttribute("aria-label")).toContain("1 new");
  });

  it("extends the track when arrivals reach minimum spacing", async () => {
    const restore = withRailWidth(200);
    try {
      const run = growingClient();
      const { container } = await mounted(run.client);
      const track = screen.getByTestId("step-bar-track");
      expect(Number.parseFloat(track.style.width)).toBeCloseTo(200, 0);

      for (const [i, call] of ["t3", "t4", "t5", "t6"].entries()) {
        run.append(call, `c${i + 2}`, 4 + i);
      }
      await refreshWalk(PID);
      await waitFor(() =>
        expect(document.querySelectorAll(".den-step-bar__dot")).toHaveLength(6),
      );

      // The pane stays fixed; the track extends into horizontal overflow.
      expect(Number.parseFloat(track.style.width)).toBeCloseTo(224, 0);
      const lefts = dotLefts(container).map((v) => Number.parseFloat(v));
      expect(Math.max(...lefts)).toBe(212);
    } finally {
      restore();
    }
  });
});

describe("a walk of one step", () => {
  it("draws no track past its only marker and lets the chapter title use the rail", async () => {
    const restore = withRailWidth(200);
    try {
      await mounted(walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 1, 1)])));
      const line = screen.getByTestId("step-bar-line");
      expect(line.style.width).toBe("0px");
      expect(line.style.left).toBe("12px");
      expect(dotLefts(document.body)).toEqual(["12px"]);
      const chapter = document.querySelector<HTMLElement>(".den-step-bar__chapter")!;
      expect(chapter.style.width).toBe("188px");
      expect(chapter.textContent).toBe("Turn 1");
    } finally {
      restore();
    }
  });
});

describe("rail pitch at the stretch boundary", () => {
  it("stretches to fill while the run is short enough to read", async () => {
    const restore = withRailWidth(400);
    try {
      await mounted();
      const track = screen.getByTestId("step-bar-track");
      expect(Number.parseFloat(track.style.width)).toBeCloseTo(400, 0);
      const lefts = dotLefts(document.body).map((v) => Number.parseFloat(v));
      expect(lefts).toHaveLength(6);
      expect(lefts[0]).toBeCloseTo(12, 5);
      expect(lefts[5]).toBeCloseTo(388, 5);
      const line = screen.getByTestId("step-bar-line");
      expect(Number.parseFloat(line.style.left)).toBeCloseTo(lefts[0]!, 5);
      expect(Number.parseFloat(line.style.width)).toBeCloseTo(lefts[5]! - lefts[0]!, 5);
      const pitch = (lefts[5]! - lefts[0]!) / 5;
      for (let i = 1; i < 5; i += 1) {
        expect(lefts[i]).toBeCloseTo(12 + pitch * i, 5);
      }
      expect(pitch).toBeGreaterThan(18);
    } finally {
      restore();
    }
  });

  it("keeps distinct markers even in a very narrow pane", async () => {
    const restore = withRailWidth(30);
    try {
      const run = growingClient();
      await mounted(run.client);
      for (const [i, call] of ["t3", "t4", "t5"].entries()) {
        run.append(call, `c${i + 2}`, 4 + i);
      }
      await refreshWalk(PID);
      await waitFor(() =>
        expect(document.querySelectorAll(".den-step-bar__dot")).toHaveLength(5),
      );

      const track = screen.getByTestId("step-bar-track");
      expect(Number.parseFloat(track.style.width)).toBe(184);
      expect(dotLefts(document.body)).toEqual([
        "12px",
        "52px",
        "92px",
        "132px",
        "172px",
      ]);
    } finally {
      restore();
    }
  });
});
