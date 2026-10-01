import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SourceWalkEffect } from "../../api/types.ts";
import { WalkStepPage, walkPageTitle } from "./WalkStepPage.tsx";
import { openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import type { WalkCommandStep, WalkGitStep, WalkGroupStep, WalkOutsideStep } from "./walk-model.ts";
import { resetWalkForTests } from "./walk-store.ts";

const PROJECT = "p1";

function effectOf(id: string, path: string, overrides: Partial<SourceWalkEffect> = {}): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: PROJECT,
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "r1",
    path,
    entry_kind: "file",
    op: "write",
    origin: "external",
    turn: 0,
    ordinal: 5,
    cause: "filesystem_reconcile",
    capture_quality: "reconciled",
    observed_at: "2026-08-26T10:00:00Z",
    ...overrides,
  });
}

function gitStep(effects: SourceWalkEffect[]): WalkGitStep {
  return {
    kind: "git",
    key: "git:t1",
    ordinal: 4,
    label: "git checkout",
    toolCallId: null,
    change: {
      session_id: "", turn: 0, tool_call_id: "", tool_name: "",
      id: "t1", root_id: "r1", kind: "checkout", from_ref: "main", to_ref: "feature-x",
      from_commit: "aaaaaaaaaaaa", to_commit: "bbbbbbbbbbbb", detail: "moving from main to feature-x",
      ordinal: 4, observed_at: "2026-08-26T10:00:00Z",
    },
    effects,
  };
}

function commandStep(effects: SourceWalkEffect[]): WalkCommandStep {
  return {
    kind: "command",
    key: "command:w1",
    ordinal: 4,
    label: "command",
    toolCallId: "call-1",
    command: {
      id: "w1", session_id: "s1", turn: 2, tool_call_id: "call-1", tool_name: "command",
      command_line: "cargo build --release", state: "running", ordinal: 4,
      started_at: "2026-08-26T10:00:00Z",
    },
    effects,
  };
}

function outsideStep(effects: SourceWalkEffect[]): WalkOutsideStep {
  return {
    kind: "outside",
    key: `outside:${effects[0]!.id}`,
    ordinal: effects[0]!.ordinal,
    label: "outside",
    toolCallId: null,
    effects,
  };
}

function mount(
  step: WalkGroupStep,
  onOpenFile: (step: WalkGroupStep, effect: SourceWalkEffect) => Promise<void> = vi.fn(async () => {}),
) {
  const key = openFilesBuffer(PROJECT, {
    kind: "walk", walkStep: step, name: walkPageTitle(step),
    rootId: "", rootLabel: "", path: "", intent: "permanent",
  });
  const buffer = projectFilesState(PROJECT).byKey[key]!;
  render(() => <WalkStepPage projectId={PROJECT} buffer={buffer} client={null} onOpenGitFile={vi.fn()} onOpenFile={onOpenFile} />);
  return { key, onOpenFile };
}

beforeEach(() => {
  resetProjectFilesForTests();
  resetWalkForTests();
});
afterEach(() => { registerNoticePublisher(null); cleanup(); });

describe("WalkStepPage", () => {
  it("keeps a group page mounted when the same step receives fresh facts", () => {
    const first = outsideStep([effectOf("o1", "a.ts")]);
    mount(first);
    const article = screen.getByTestId("walk-page").querySelector("article");
    const heading = screen.getByTestId("walk-page-title");
    const updated = { ...first, effects: [...first.effects, effectOf("o2", "b.ts")] };
    openFilesBuffer(PROJECT, {
      kind: "walk", walkStep: updated, name: walkPageTitle(updated),
      rootId: "", rootLabel: "", path: "", intent: "permanent",
    });
    expect(screen.getAllByTestId("walk-page-file")).toHaveLength(2);
    expect(screen.getByTestId("walk-page").querySelector("article")).toBe(article);
    expect(screen.getByTestId("walk-page-title")).toBe(heading);
  });

  it("names the tab after the step", () => {
    expect(walkPageTitle(gitStep([]))).toBe("Git checkout · main → feature-x");
    expect(walkPageTitle(commandStep([]))).toBe("cargo build --release");
    expect(walkPageTitle(outsideStep([effectOf("o1", "a.ts")]))).toBe("Outside the app");
  });

  it("lists outside files by path and opens one on click", () => {
    const { onOpenFile } = mount(outsideStep([
      effectOf("cg2", "src/zeta.ts", { ordinal: 6 }),
      effectOf("cg1", "src/alpha.ts", { op: "create" }),
    ]));

    expect(screen.getByTestId("walk-page-title").textContent).toBe("Outside the app");
    const rows = screen.getAllByTestId("walk-page-file");
    expect(rows.map((row) => row.getAttribute("data-path"))).toEqual(["src/alpha.ts", "src/zeta.ts"]);
    expect(rows[0]!.textContent).toContain("alpha.ts");
    expect(rows[0]!.textContent).toContain("src");

    fireEvent.click(rows[1]!);
    expect(onOpenFile).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "outside" }),
      expect.objectContaining({ id: "cg2" }),
    );
  });

  it("says a picked version is opening, and reports why it could not open to the project's notices", async () => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    let fail!: (error: Error) => void;
    mount(
      outsideStep([effectOf("o1", "a.ts")]),
      () => new Promise<void>((_resolve, reject) => { fail = reject; }),
    );
    const row = screen.getByTestId("walk-page-file");

    fireEvent.click(row);
    expect(row.getAttribute("aria-busy")).toBe("true");
    expect(row.textContent).toContain("Opening…");

    fail(new Error("Comparison unavailable"));
    await waitFor(() => expect(
      selectProjectNoticeGroups(notices.index()).flatMap(group => group.notices).map(notice => notice.message),
    ).toEqual(["Couldn't open this version: Comparison unavailable"]));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(row.getAttribute("aria-busy")).toBe("false");
    expect(row.textContent).toContain("Open this version");
  });

  it("lists a file written twice in one run once, at its latest version", () => {
    const { onOpenFile } = mount(outsideStep([
      effectOf("o1", "README.md", { ordinal: 5, after_version_id: "v-old" }),
      effectOf("o2", "README.md", { ordinal: 6, after_version_id: "v-new" }),
      effectOf("o3", "docs/guide.md", { ordinal: 7 }),
    ]));
    const rows = screen.getAllByTestId("walk-page-file");
    expect(rows.map((row) => row.getAttribute("data-path"))).toEqual(["docs/guide.md", "README.md"]);
    expect(screen.getByTestId("walk-page-meta").textContent).toContain("Files2");
    fireEvent.click(rows[1]!);
    expect(onOpenFile).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ id: "o2" }));
  });

  it("opens a Git review even when the movement has no file effects", () => {
    mount(gitStep([]));
    expect(screen.getByTestId("walk-git-review")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("walk-page-empty")).toBeNull();
  });

  it("presents a command with its honesty line and running state", () => {
    mount(commandStep([effectOf("cw1", "Cargo.lock", { cause: "command_window", command_id: "w1" })]));
    expect(screen.getByTestId("walk-page-title").textContent).toBe("Command");
    expect(screen.getByTestId("walk-page-subtitle").textContent).toBe("cargo build --release");
    expect(screen.getByTestId("walk-page-lede").textContent).toContain("may not have written them all");
    expect(screen.getByTestId("walk-page-state").textContent).toContain("Still running");
    expect(screen.getByTestId("walk-page-meta").textContent).toContain("Turn");
  });

  it("presents an outside run with what it claims and no more", () => {
    mount(outsideStep([effectOf("o1", "gen/x.ts"), effectOf("o2", "gen/y.ts", { ordinal: 6 })]));
    expect(screen.getByTestId("walk-page-title").textContent).toBe("Outside the app");
    expect(screen.getByTestId("walk-page-lede").textContent)
      .toContain("Nothing in this chat wrote them");
    expect(screen.getAllByTestId("walk-page-file")).toHaveLength(2);
  });

  it("reads as a closed walk's page once the walk is gone", () => {
    mount(outsideStep([effectOf("o1", "gen/x.ts")]));
    expect(screen.getByTestId("walk-page-position").textContent).toContain("Walk closed");
  });
});
