import { cleanup, fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceWalkResponse } from "../../api/types.ts";
import { stubFilesClient, type ComparisonLoader } from "../../test/source-client-fixture.ts";
import type { SourceComparisonTarget } from "../../api/http-capabilities/source-history.ts";
import { EditorView } from "@codemirror/view";
import { DiffsPage } from "./DiffsPage.tsx";
import type { DiffsAddress } from "./diffs-address.ts";
import { resetTurnDiffsForTests } from "./turn-diffs.ts";
import { resolveScope, resetScopeResolutionForTests } from "../tree/scope-resolution.ts";
import { chooseSidebarScope, resetFilesStagePaneForTests, setComparisonOff } from "./review-pane.ts";
import { walkEffectFixture } from "../walk/walk-fixtures.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import { mockReaderViewport } from "../../test/reader-viewport-mock.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { resetGitDiffsForTests } from "./git-diffs.ts";
import * as layoutBands from "../../layout/layout-bands.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { diffWordWrapPref, saveDiffWordWrap } from "../../settings/appearance/display-prefs.ts";

const TURN: DiffsAddress = { kind: "turn", sessionId: "s1", turn: 4, messageId: "s1-user-4" };
const LENS: DiffsAddress = { kind: "lens" };
const GIT: DiffsAddress = {
  kind: "git", rootId: "r1", spec: "a1b2c3d", label: "a1b2c3d Fix the loader",
  beforeCommit: "b".repeat(40), afterCommit: "a1b2c3d".padEnd(40, "0"),
};

function fileFixture(path: string, ordinals: number[]): SourceWalkResponse["files"][number] {
  return {
    file_id: `file-${path}`, root_id: "r1", path,
    changed_since_presented: false, unpresented_agent_effects: 0,
    tip: { state: "content", sha256: "tip" }, head_match: "unknown",
    effects: ordinals.map((ordinal) => ({ ...walkEffectFixture(`${path}-${ordinal}`, 4, ordinal), file_id: `file-${path}`, path })),
  };
}

function clientFixture(files: SourceWalkResponse["files"]): LycaonClient {
  return stubFilesClient({
    listProjectSourceWalk: async (_projectId: string, opts?: { baseline?: string }) => ({
      baseline: opts?.baseline ?? "presentation",
      files, turns: [], commands: [], git_changes: [],
      commit_available: true,
    }),
    readComparison: (async () => ({
      in_range: true, location_changed: false,
      before: { state: "content", size_bytes: 4, availability: "available", content: "old\n" },
      after: { state: "content", size_bytes: 4, availability: "available", content: "new\n" },
    })) as ComparisonLoader,
  });
}

afterEach(() => {
  cleanup();
  void saveDiffWordWrap(false);
  registerNoticePublisher(null);
  resetTurnDiffsForTests();
  resetGitDiffsForTests();
  resetScopeResolutionForTests();
  resetFilesStagePaneForTests();
});

describe("the diffs page", () => {

  it("reads a Git page from two commits, counting lines but no writes", async () => {
    const reader = sourceReaderFixture(async (_project, source) => {
      if (source.kind !== "git_range") throw new Error(`Unexpected selector ${source.kind}`);
      return {
        in_range: true, location_changed: false,
        before: { state: "content", size_bytes: 4, availability: "available", content: "old\n" },
        after: { state: "content", size_bytes: 4, availability: "available", content: "new\n" },
      };
    });
    const file = (path: string) => ({
      path, before_path: path, op: "write" as const, before_mode: "100644", after_mode: "100644",
      before_oid: "1".repeat(40), after_oid: "2".repeat(40), insertions: 1, deletions: 1, binary: false,
    });
    const getProjectSourceRevisionReview = vi.fn(async () => ({
      root_id: "r1", before_commit: "b".repeat(40), after_commit: "a1b2c3d".padEnd(40, "0"),
      files: [file("a.go"), file("b.go")], files_total: 2, insertions: 2, deletions: 2,
    }));
    const client = stubClient({ ...reader.methods, getProjectSourceRevisionReview });

    render(() => <DiffsPage projectId="p1" client={client} address={GIT} />);

    expect(await screen.findByText("2 files changed")).toBeTruthy();
    expect(screen.getByTestId("diffs-page-comparison").textContent).toBe("a1b2c3d Fix the loader");
    await waitFor(() => expect(screen.getByTestId("diffs-page-stat").textContent).toBe("+2−2"));
    expect(screen.queryByTestId("diffs-page-walk")).toBeNull();
    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(2));
  });

  it("counts a turn's files and writes, and opens every diff", async () => {
    const client = clientFixture([fileFixture("a.go", [3, 4]), fileFixture("b.go", [5])]);

    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    expect(await screen.findByText("2 files changed")).toBeTruthy();
    expect(screen.getByTestId("diffs-page-comparison").textContent).toBe("What this turn did");
    expect(screen.getByTestId("diffs-page-stat").textContent).toContain("3 writes");
    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(2));
    for (const section of screen.getAllByTestId("diffs-section-header")) {
      expect(section.getAttribute("data-expanded")).toBe("true");
    }
  });

  it("reads every file through one editor", async () => {
    render(() => (
      <DiffsPage projectId="p1" client={clientFixture([fileFixture("a.go", [3]), fileFixture("b.go", [1])])} address={TURN} onWalkTurn={() => {}} />
    ));

    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(2));
    const page = screen.getByTestId("diffs-page");
    // Page heading and file sections are blocks in one editor document.
    expect(page.querySelectorAll(".cm-editor")).toHaveLength(1);
    expect(page.querySelector(".cm-content .den-diffs-page__article")).toBeTruthy();
    for (const section of screen.getAllByTestId("diffs-section-header")) {
      expect(section.closest(".cm-content")).toBeTruthy();
    }
  });

  it("gives every file its place before any of them loads", async () => {
    const files = Array.from({ length: 60 }, (_, index) => fileFixture(`f${index}.go`, [index + 1]));
    const client = clientFixture(files);
    const opened = vi.spyOn(client, "createSourceView");

    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    expect(await screen.findByText("60 files changed")).toBeTruthy();
    // Every file is on the page from the start, sized by the host's measure.
    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(60));
    // Far fewer open a comparison: only what comes near the viewport.
    expect(new Set(opened.mock.calls.map(([, request]) => JSON.stringify(request))).size).toBeLessThan(files.length);
  });

  it("totals every file from the host's measure, including rows never mounted", async () => {
    const client = clientFixture(Array.from({ length: 60 }, (_, index) => fileFixture(`f${index}.go`, [index + 1])));
    const digest = vi.spyOn(client, "digestSourceComparisons");

    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    await waitFor(() => expect(screen.getByTestId("diffs-page-stat").textContent).toContain("+60"));
    expect(screen.getByTestId("diffs-page-stat").textContent).toContain("−60");
    // Sixty comparisons, measured in one request.
    expect(digest).toHaveBeenCalledTimes(1);
    expect(digest.mock.calls[0]![1].sources).toHaveLength(60);
  });

  it("says a file came back unchanged instead of drawing an empty diff", async () => {
    const client = stubFilesClient({
      listProjectSourceWalk: async (_projectId: string, opts?: { baseline?: string }) => ({
        baseline: opts?.baseline ?? "presentation",
        files: [fileFixture("same.go", [3])], turns: [], commands: [], git_changes: [],
        commit_available: true,
      }),
      readComparison: (async () => ({
        in_range: true, location_changed: false,
        before: { state: "content", size_bytes: 4, availability: "available", content: "same\n" },
        after: { state: "content", size_bytes: 4, availability: "available", content: "same\n" },
      })) as ComparisonLoader,
    });
    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    expect(await screen.findByText("no net change")).toBeTruthy();
    expect(screen.getByText("This comparison leaves the file as it found it.")).toBeTruthy();
    expect(screen.queryByTestId("source-reader")).toBeNull();
  });

  it("presents a binary file with binary status and explains it cannot be shown as text", async () => {
    const file = {
      ...fileFixture("image.png", [1]),
      commit: { head: "1".repeat(40), status: "M ", op: "write" as const, availability: "binary", history_truncated: false },
    };
    const client = stubFilesClient({
      listProjectSourceWalk: async (_projectId: string, opts?: { baseline?: string }) => ({
        baseline: opts?.baseline ?? "presentation",
        files: [file], turns: [], commands: [], git_changes: [],
        commit_available: true,
      }),
      digestSourceComparisons: async () => ({
        digests: [{
          in_range: true,
          changes_rows: 0,
          summary: {
            added: 0,
            removed: 0,
            rows: 0,
            change_areas: [],
            change_area_count: 0,
            before: { path: "image.png", sha256: "b1", lines: 0, availability: "binary" },
            after: { path: "image.png", sha256: "b2", lines: 0, availability: "binary" },
          },
        }],
      }),
      readComparison: (async () => ({
        in_range: true, location_changed: false,
        before: { state: "content", size_bytes: 100, availability: "binary", content: "" },
        after: { state: "content", size_bytes: 120, availability: "binary", content: "" },
      })) as ComparisonLoader,
    });
    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    expect(await screen.findByText("binary")).toBeTruthy();
    expect(screen.getByText("This file is binary and cannot be shown as a text diff.")).toBeTruthy();
    expect(screen.queryByText("This comparison leaves the file as it found it.")).toBeNull();
    expect(screen.queryByTestId("source-reader")).toBeNull();
  });

  it("folds every file together, and unfolds them again", async () => {
    const client = clientFixture(Array.from({ length: 30 }, (_, index) => fileFixture(`f${index}.go`, [index + 1])));
    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);
    const fold = await screen.findByTestId("diffs-page-fold");

    fold.click();
    await waitFor(() => {
      for (const section of screen.getAllByTestId("diffs-section-header")) expect(section.getAttribute("data-expanded")).toBe("false");
    });
    expect(fold.textContent).toBe("Expand all");

    fold.click();
    await waitFor(() => {
      for (const section of screen.getAllByTestId("diffs-section-header")) expect(section.getAttribute("data-expanded")).toBe("true");
    });
  });

  it("answers page commands only from the page on display", async () => {
    const client = clientFixture([fileFixture("a.go", [3]), fileFixture("b.go", [5])]);
    render(() => (
      <>
        <ResidentPresenceProvider presence="active">
          <div data-testid="shown-page"><DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} /></div>
        </ResidentPresenceProvider>
        <ResidentPresenceProvider presence="pending">
          <div data-testid="retained-page"><DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} /></div>
        </ResidentPresenceProvider>
      </>
    ));
    const shown = screen.getByTestId("shown-page"), retained = screen.getByTestId("retained-page");
    const shownFold = await within(shown).findByTestId("diffs-page-fold");
    const retainedFold = await within(retained).findByTestId("diffs-page-fold");

    expect(invokeCommand("files.diffToggleCollapse")).toBe("ran");
    await waitFor(() => expect(shownFold.textContent).toBe("Expand all"));
    expect(retainedFold.textContent).not.toBe("Expand all");
  });

  it("opens a folded run through the section's own presentation", async () => {
    const source = Array.from({ length: 1000 }, (_, index) => `line ${index}\n`).join("");
    const reader = sourceReaderFixture(async () => ({
      in_range: true, location_changed: false,
      before: { state: "content", size_bytes: source.length, availability: "available", content: source },
      after: { state: "content", size_bytes: source.length, availability: "available",
        content: source.replace("line 500\n", "changed\n") },
    }));
    const getProjectSourceRevisionReview = vi.fn(async () => ({
      root_id: "r1", before_commit: "b".repeat(40), after_commit: "a1b2c3d".padEnd(40, "0"),
      files: [{
        path: "a.go", before_path: "a.go", op: "write" as const, before_mode: "100644", after_mode: "100644",
        before_oid: "1".repeat(40), after_oid: "2".repeat(40), insertions: 1, deletions: 1, binary: false,
      }], files_total: 1, insertions: 1, deletions: 1,
    }));
    const client = stubClient({ ...reader.methods, getProjectSourceRevisionReview });
    const updates = vi.spyOn(client, "applySourceViewIntent");
    onTestFinished(mockReaderViewport());

    render(() => <DiffsPage projectId="p1" client={client} address={GIT} />);

    // The comparison folds the unchanged runs on either side of the one change.
    const chips = await screen.findAllByText("Show lines");
    fireEvent.click(chips[0]!);

    // Unfold through presentation update rather than re-reading the window.
    await waitFor(() => expect(updates).toHaveBeenCalled());
    expect(updates.mock.calls.at(-1)?.[2]).toMatchObject({
      kind: "comparison", intent: { mode: "changes", expanded: [{ start: 0 }] },
    });
    const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
    await waitFor(() => expect(view.state.doc.toString()).toContain("line 0\nline 1\n"));
  });

  it("declares a file's heading atomic, so pointing at it never selects it", async () => {
    render(() => (
      <DiffsPage projectId="p1" client={clientFixture([fileFixture("a.go", [3])])} address={TURN} onWalkTurn={() => {}} />
    ));
    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(1));

    const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
    const heading = screen.getByTestId("diffs-section-header").closest(".cm-den-block")!;
    const at = view.posAtDOM(heading);

    // Headings are atomic replacements skipped during caret placement.
    const atoms = view.state.facet(EditorView.atomicRanges).map(source => source(view));
    const covered = atoms.some(set => {
      let found = false;
      set.between(at, at + 1, () => { found = true; });
      return found;
    });
    expect(covered).toBe(true);
  });

  it("frames a file's heading to the scroller, not to the unwrapped lines beside it", async () => {
    render(() => (
      <DiffsPage projectId="p1" client={clientFixture([fileFixture("a.go", [3])])} address={TURN} onWalkTurn={() => {}} />
    ));
    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(1));

    const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
    Object.defineProperty(view.scrollDOM, "clientWidth", { value: 640, configurable: true });
    Object.defineProperty(view.contentDOM, "offsetLeft", { value: 48, configurable: true });
    (view as unknown as { measure: () => void }).measure();

    const heading = screen.getByTestId("diffs-section-header").closest<HTMLElement>(".cm-den-block")!;
    expect(heading.style.getPropertyValue("--den-editor-block-left")).toBe("48px");
    expect(heading.style.getPropertyValue("--den-editor-block-w")).toBe("592px");
  });

  it("names a file's disclosure by the state the band is in", async () => {
    render(() => (
      <DiffsPage projectId="p1" client={clientFixture([fileFixture("a.go", [3])])} address={TURN} onWalkTurn={() => {}} />
    ));

    const band = await screen.findByTestId("diffs-section-header");
    const disclosure = band.querySelector<HTMLButtonElement>(".den-file-edit-diff-disclosure")!;
    expect(disclosure.getAttribute("aria-expanded")).toBe("true");
    expect(disclosure.getAttribute("aria-label")).toBe("Collapse diff for a.go");

    disclosure.click();
    await waitFor(() => expect(band.getAttribute("data-expanded")).toBe("false"));
    expect(disclosure.getAttribute("aria-expanded")).toBe("false");
    expect(disclosure.getAttribute("aria-label")).toBe("Show diff for a.go");
  });

  it("carries a turn's walk back to the chapter the card opens", async () => {
    const walk = vi.fn();
    render(() => (
      <DiffsPage projectId="p1" client={clientFixture([fileFixture("a.go", [3])])} address={TURN} onWalkTurn={walk} />
    ));

    (await screen.findByTestId("diffs-page-walk")).click();
    expect(walk).toHaveBeenCalledWith(TURN);
  });

  it("reads the panel's own resolved scope, and follows the eye when it moves", async () => {
    const client = clientFixture([fileFixture("a.go", [3]), fileFixture("b.go", [4])]);
    await resolveScope("p1", client, null);

    render(() => <DiffsPage projectId="p1" client={client} address={LENS} />);

    expect(await screen.findByText("2 files changed")).toBeTruthy();
    expect(screen.getByTestId("diffs-page-comparison").textContent).toBe("New since you looked");
    // No walk from a comparison that is not a turn.
    expect(screen.queryByTestId("diffs-page-walk")).toBeNull();

    chooseSidebarScope("p1", { kind: "commit" });
    await resolveScope("p1", client, null);
    await waitFor(() =>
      expect(screen.getByTestId("diffs-page-comparison").textContent).toBe("Changes since last commit"),
    );
  });

  it("says nothing is marked in the lens's own words when the eye is off", async () => {
    const client = clientFixture([fileFixture("a.go", [3])]);
    setComparisonOff("p1", true);
    await resolveScope("p1", client, null);

    render(() => <DiffsPage projectId="p1" client={client} address={LENS} />);

    const empty = await screen.findByTestId("diffs-page-empty");
    expect(empty.textContent).toContain("Marking is off");
    expect(screen.getByTestId("diffs-page-comparison").textContent).toBe("Nothing marked");
  });

  it("claims nothing before the comparison settles", () => {
    render(() => <DiffsPage projectId="p1" client={clientFixture([])} address={LENS} />);

    expect(screen.getByTestId("diffs-page-title").textContent).toContain("Reading this comparison");
    expect(screen.queryByTestId("diffs-page-empty")).toBeNull();
  });

  it("claims no empty comparison without a connection", () => {
    render(() => <DiffsPage projectId="p1" client={null} address={TURN} onWalkTurn={() => {}} />);

    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("diffs-page-empty")).toBeNull();
  });

  it("reports a failed turn read as a notice, with no empty claim or inline alert", async () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    const client = stubFilesClient({
      listProjectSourceWalk: async () => { throw new Error("walk offline"); },
    });

    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    await waitFor(() => {
      const rows = selectProjectNoticeGroups(store.index()).find((group) => group.projectId === "p1")?.notices ?? [];
      expect(rows).toMatchObject([{ code: "files_diffs_unavailable", message: "walk offline" }]);
    });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("diffs-page-empty")).toBeNull();
    registerNoticePublisher(null);
  });

  it("keeps revision strip visible and resolves comparison stat when navigating revisions of a file created and deleted", async () => {
    const fileWithCreateAndDelete: SourceWalkResponse["files"][number] = {
      file_id: "file-held", root_id: "r1", path: "held_sockets.txt",
      changed_since_presented: false, unpresented_agent_effects: 0,
      tip: { state: "absent" }, head_match: "unknown",
      effects: [
        { ...walkEffectFixture("held-1", 4, 1), file_id: "file-held", path: "held_sockets.txt", op: "create" },
        { ...walkEffectFixture("held-2", 4, 2), file_id: "file-held", path: "held_sockets.txt", op: "delete" },
      ],
    };
    const client = stubFilesClient({
      listProjectSourceWalk: async () => ({
        baseline: "session:s1",
        files: [fileWithCreateAndDelete],
        turns: [], commands: [], git_changes: [],
        commit_available: true,
      }),
      readComparison: (async (_project: string, target: SourceComparisonTarget) => {
        if ("fileId" in target) {
          return {
            in_range: true, location_changed: false,
            before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
            after: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
          };
        }
        if ("effectId" in target) {
          if (target.effectId === "held-1") {
            return {
              in_range: true, location_changed: false,
              before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
              after: { state: "content", size_bytes: 12, availability: "available", content: "socket data\n" },
            };
          }
          if (target.effectId === "held-2") {
            return {
              in_range: true, location_changed: false,
              before: { state: "content", size_bytes: 12, availability: "available", content: "socket data\n" },
              after: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
            };
          }
        }
        throw new Error(`Unexpected target ${JSON.stringify(target)}`);
      }) as unknown as ComparisonLoader,
    });

    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    expect(await screen.findByText("no net change")).toBeTruthy();
    expect(screen.getByText("This comparison leaves the file as it found it.")).toBeTruthy();
    expect(screen.getByText("2 edits")).toBeTruthy();

    const strip = screen.getByTestId("file-edit-diff-strip");
    const steps = strip.querySelectorAll<HTMLButtonElement>(".den-file-edit-diff-step");
    expect(steps).toHaveLength(3);
    expect(steps[0]?.getAttribute("aria-pressed")).toBe("true");
    expect(steps[1]?.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(steps[1]!);

    await waitFor(() => expect(steps[1]?.getAttribute("aria-pressed")).toBe("true"));
    expect(steps[0]?.getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByTestId("file-edit-diff-strip")).toBeTruthy();

    await waitFor(() => {
      const stat = screen.getByTestId("file-edit-diff-stat");
      expect(stat.textContent).toContain("+1");
      expect(stat.textContent).toContain("−0");
    });

    const band = screen.getByTestId("diffs-section-header");
    expect(band.querySelector(".den-file-edit-diff-fullscreen-btn")).toBeTruthy();
    expect(band.querySelector(".den-file-edit-diff-disclosure")).toBeTruthy();

    fireEvent.click(steps[0]!);
    await waitFor(() => expect(steps[0]?.getAttribute("aria-pressed")).toBe("true"));
    expect(screen.getByText("no net change")).toBeTruthy();
  });

  it("binds diffs layout band for responsive adaptation", async () => {
    const bindSpy = vi.spyOn(layoutBands, "bindLayoutBand");
    const client = clientFixture([fileFixture("a.go", [3, 4])]);
    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    const page = screen.getByTestId("diffs-page");
    expect(bindSpy).toHaveBeenCalledWith(page, layoutBands.LAYOUT_BAND_SCALES.diffsPage);
  });

  it("shows an added file and a deleted file with stats details instead of whole diff rows", async () => {
    const reader = sourceReaderFixture(async (_project, source) => {
      if (source.kind !== "git_range") throw new Error(`Unexpected selector ${source.kind}`);
      return {
        in_range: true, location_changed: false,
        before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
        after: { state: "content", size_bytes: 14, availability: "available", content: "hello world\n" },
      };
    });
    const files = [
      {
        path: "created.py", before_path: "created.py", op: "create" as const, before_mode: "100644", after_mode: "100644",
        before_oid: "0".repeat(40), after_oid: "2".repeat(40), insertions: 5, deletions: 0, binary: false,
      },
      {
        path: "removed.go", before_path: "removed.go", op: "delete" as const, before_mode: "100644", after_mode: "100644",
        before_oid: "1".repeat(40), after_oid: "0".repeat(40), insertions: 0, deletions: 8, binary: false,
      },
    ];
    const getProjectSourceRevisionReview = vi.fn(async () => ({
      root_id: "r1", before_commit: "b".repeat(40), after_commit: "a1b2c3d".padEnd(40, "0"),
      files, files_total: 2, insertions: 5, deletions: 8,
    }));
    const client = stubClient({ ...reader.methods, getProjectSourceRevisionReview });

    render(() => <DiffsPage projectId="p1" client={client} address={GIT} />);

    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(2));
    const section = (path: string) => document.querySelector<HTMLElement>(`.den-diffs-section[data-path="${path}"]`)!;

    await waitFor(() => {
      expect(section("created.py").querySelector('[data-testid="file-edit-diff-details"]')).toBeTruthy();
      expect(section("removed.go").querySelector('[data-testid="file-edit-diff-details"]')).toBeTruthy();
    });

    expect(section("created.py").querySelector(".den-file-edit-diff-details-badge")?.textContent).toBe("New file");
    expect(section("created.py").querySelector(".den-file-edit-diff-details-lang")?.textContent).toBe("Python");
    expect(section("created.py").querySelector(".den-file-edit-diff-details-lines")?.textContent).toBe("5 lines added");
    expect(section("created.py").querySelector(".den-file-edit-diff-details-btn")?.textContent).toBe("View file");

    expect(section("removed.go").querySelector(".den-file-edit-diff-details-badge")?.textContent).toBe("Deleted file");
    expect(section("removed.go").querySelector(".den-file-edit-diff-details-lang")?.textContent).toBe("Go");
    expect(section("removed.go").querySelector(".den-file-edit-diff-details-lines")?.textContent).toBe("8 lines removed");
    expect(section("removed.go").querySelector(".den-file-edit-diff-details-btn")?.textContent).toBe("View removed contents");

    const createdDisclosure = section("created.py").querySelector<HTMLButtonElement>(".den-file-edit-diff-disclosure")!;
    createdDisclosure.click();
    await waitFor(() => expect(section("created.py").getAttribute("data-expanded")).toBe("false"));
    expect(section("created.py").querySelector('[data-testid="file-edit-diff-details"]')).toBeNull();

    createdDisclosure.click();
    await waitFor(() => expect(section("created.py").getAttribute("data-expanded")).toBe("true"));
    expect(section("created.py").querySelector('[data-testid="file-edit-diff-details"]')).toBeTruthy();
  });

  it("resets section collapsed state and reader state when switching comparison view", async () => {
    const client = clientFixture([fileFixture("a.go", [3, 4]), fileFixture("b.go", [5])]);
    const [address, setAddress] = createSignal<DiffsAddress>(TURN);

    render(() => <DiffsPage projectId="p1" client={client} address={address()} onWalkTurn={() => {}} />);

    expect(await screen.findByText("2 files changed")).toBeTruthy();
    await waitFor(() => expect(screen.getAllByTestId("diffs-section-header")).toHaveLength(2));

    const disclosures = screen.getAllByRole("button", { name: /Collapse/i });
    expect(disclosures.length).toBeGreaterThan(0);
    fireEvent.click(disclosures[0]!);

    await waitFor(() => {
      expect(screen.getAllByRole("button", { name: /Expand/i })).toHaveLength(1);
    });

    const nextTurn: DiffsAddress = { kind: "turn", sessionId: "s1", turn: 5, messageId: "s1-user-5" };
    setAddress(nextTurn);

    await waitFor(() => {
      expect(screen.queryAllByRole("button", { name: /Expand/i })).toHaveLength(0);
    });
  });

  it("renders a status bar with wrap toggle and updates wrap preference reactively", async () => {
    const client = clientFixture([fileFixture("a.go", [3, 4]), fileFixture("b.go", [5])]);
    await saveDiffWordWrap(false);

    render(() => <DiffsPage projectId="p1" client={client} address={TURN} onWalkTurn={() => {}} />);

    const status = await screen.findByTestId("diffs-page-status");
    expect(status).toBeTruthy();
    expect(screen.getByTestId("diffs-editor-mode").textContent).toBe("All diffs");
    expect((await screen.findByTestId("diffs-status-files")).textContent).toBe("2 files");

    const wrapBtn = screen.getByTestId("diffs-page-wrap-toggle");
    expect(wrapBtn.getAttribute("aria-pressed")).toBe("false");

    const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
    const wraps = () =>
      view.state
        .facet(EditorView.contentAttributes)
        .some((v) => typeof v !== "function" && v.class === "cm-lineWrapping");

    expect(wraps()).toBe(false);

    fireEvent.click(wrapBtn);
    await waitFor(() => expect(wrapBtn.getAttribute("aria-pressed")).toBe("true"));
    expect(diffWordWrapPref()).toBe(true);
    await waitFor(() => expect(wraps()).toBe(true));

    fireEvent.click(wrapBtn);
    await waitFor(() => expect(wrapBtn.getAttribute("aria-pressed")).toBe("false"));
    expect(diffWordWrapPref()).toBe(false);
    await waitFor(() => expect(wraps()).toBe(false));
  });
});
