import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { stubFilesClient as stubClient, type FileClientStubs } from "../../test/source-client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { ReviewLensHost as ReviewLens } from "./review-lens-test-host.tsx";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { LycaonApiError } from "../../api/http.ts";
import {
  requestedFirstTimeTips,
  resetFirstTimeTipRequestsForTests,
} from "../../first-time-tips/first-time-tips-service.ts";
import {
  chooseSidebarScope,
  resetFilesStagePaneForTests,
  setComparisonOff,
} from "./review-pane.ts";
import { resetReviewLiveForTests, syncReviewPresence } from "./review-live.ts";
import type { AgentSessionPresence } from "../../api/types.ts";
import {
  registerOpenFilesSurfaceSink,
  resetOpenFilesSurfaceForTests,
} from "../../platform/navigation/open-files-surface.ts";
import {
  resetScopeResolutionForTests,
  resolveScope,
} from "../tree/scope-resolution.ts";
import {
  enterWalk,
  leaveWalk,
  resetWalkForTests,
  setWalkAt,
} from "../walk/walk-store.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceSeenFile, SourceWalkEffect, SourceWalkFile } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

function walkEffect(
  id: string,
  path: string,
  ordinal = 1,
  overrides: Partial<SourceWalkEffect> = {},
): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: "p1",
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "r1",
    path,
    op: "write",
    entry_kind: "file",
    origin: "agent",
    turn: 1,
    ordinal,
    cause: "tool",
    capture_quality: "exact",
    observed_at: `2026-08-23T10:0${ordinal}:00Z`,
    ...overrides,
  });
}

function walkFile(
  path: string,
  effects: SourceWalkEffect[],
  overrides: Partial<SourceWalkFile> = {},
): SourceWalkFile {
  return {
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    changed_since_presented: effects.length > 0,
    unpresented_agent_effects: effects.some((effect) => effect.origin === "agent")
      ? 1
      : 0,
    tip: { state: "content", sha256: "tip" },
    head_match: "unknown",
    effects,
    ...overrides,
  };
}

function mockClient(overrides: FileClientStubs = {}): LycaonClient {
  return stubClient({
    readComparison: vi.fn().mockResolvedValue({
      in_range: true,
      before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
      after: { state: "content", size_bytes: 0, availability: "available", content: "" },
      location_changed: false,
    }),
    listProjectSourceWalk: vi.fn().mockResolvedValue({
      files: [],
      commit_available: false,
      git_changes: [], commands: [], turns: [],
    }),
    listProjectSourcePins: vi.fn().mockResolvedValue({ pins: [] }),
    getProjectSourceStorage: vi.fn().mockResolvedValue({
      inventory: {
        status: "ready",
        complete: true,
        roots_generation: 1,
        indexed_files: 0,
      },
      storage: {
        lanes: [{ lane: "source_blobs", scope: "device", used_bytes: 0 }],
      },
    }),
    completeProjectSourcePresentation: vi.fn().mockResolvedValue(undefined),
    listProjectSourceSeen: vi.fn().mockResolvedValue({ files: [], next_cursor: undefined }),
    ...overrides,
  });
}

function mockAppStore(): AppStore {
  return {
    state: {
      currentSession: { id: "s1", title: "Focus" },
      messages: [
        { id: "m1", role: "user", content: "hi", visibility: "user" },
      ],
      workers: [],
    },
  } as unknown as AppStore;
}

let notices = createNoticeStore();
const noticeRows = () => selectProjectNoticeGroups(notices.index()).find((group) => group.projectId === "p1")?.notices ?? [];
beforeEach(() => {
  notices = createNoticeStore();
  registerNoticePublisher(notices);
});
afterEach(() => registerNoticePublisher(null));

describe("ReviewLens", () => {
  afterEach(() => {
    cleanup();
    resetFilesStagePaneForTests();
    resetScopeResolutionForTests();
    resetWalkForTests();
    resetOpenFilesSurfaceForTests();
  });

  it("opens all the comparison's diffs from the one control that speaks for the list", async () => {
    const sink = vi.fn();
    registerOpenFilesSurfaceSink(sink);
    chooseSidebarScope("p1", { kind: "commit" });
    render(() => (
      <ReviewLens
        projectId="p1"
        appStore={mockAppStore()}
        client={mockClient({
          listProjectSourceWalk: vi.fn().mockResolvedValue({
            files: [walkFile("a.ts", [walkEffect("e1", "a.ts")])],
            commit_available: true,
            git_changes: [], commands: [], turns: [],
          }),
        })}
        onOpenFile={vi.fn()}
      />
    ));

    const door = await screen.findByTestId("review-lens-open-diffs");
    await waitFor(() => expect((door as HTMLButtonElement).disabled).toBe(false));
    // The control names what it opens, so a disabled one can still say why.
    expect(door.getAttribute("aria-label")).toBe("Open all diffs · since last commit");
    fireEvent.click(door);

    expect(sink).toHaveBeenCalledWith({
      kind: "diffs", projectId: "p1", address: { kind: "lens" },
    });
  });

  it("offers no diffs page with marking off, and says why", async () => {
    setComparisonOff("p1", true);
    render(() => (
      <ReviewLens projectId="p1" appStore={mockAppStore()} client={mockClient()} onOpenFile={vi.fn()} />
    ));

    const door = await screen.findByTestId("review-lens-open-diffs");
    expect((door as HTMLButtonElement).disabled).toBe(true);
    expect(door.getAttribute("aria-label")).toBe("Marking is off — choose a comparison first");
  });

  it("keeps recorded changes available while repository inventory runs", async () => {
    render(() => (
      <ReviewLens
        projectId="p1"
        appStore={mockAppStore()}
        client={mockClient({
          getProjectSourceStorage: vi.fn().mockResolvedValue({
            inventory: {
              status: "scanning",
              complete: false,
              roots_generation: 2,
              indexed_files: 0,
            },
            storage: {
              lanes: [{ lane: "source_blobs", scope: "device", used_bytes: 0 }],
            },
          }),
        })}
        onOpenFile={vi.fn()}
      />
    ));

    expect(
      (await screen.findByTestId("review-inventory-status")).textContent,
    ).toContain("Indexing repository history in the background");
    expect(screen.getByTestId("review-lens-list")).toBeTruthy();
  });

  it("reports a failed inventory refresh to the project's notices and keeps the list", async () => {
    render(() => (
      <ReviewLens
        projectId="p1"
        appStore={mockAppStore()}
        client={mockClient({
          getProjectSourceStorage: vi.fn().mockResolvedValue({
            inventory: { status: "error", complete: false, roots_generation: 2, indexed_files: 0 },
            storage: { lanes: [{ lane: "source_blobs", scope: "device", used_bytes: 0 }] },
          }),
        })}
        onOpenFile={vi.fn()}
      />
    ));

    await waitFor(() => expect(noticeRows().map((notice) => notice.code)).toContain("files_inventory_unavailable"));
    expect(screen.queryByTestId("review-inventory-status")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByTestId("review-lens-list")).toBeTruthy();
  });

  it("keeps empty copy visible while a held response refreshes", async () => {
    let resolveSecond: ((value: unknown) => void) | undefined;
    const listProjectSourceWalk = vi
      .fn()
      .mockResolvedValueOnce({
        files: [],
        commit_available: false,
        git_changes: [], commands: [], turns: [],
      })
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      );
    const client = mockClient({ listProjectSourceWalk });

    render(() => (
      <ReviewLens
        projectId="p1"
        appStore={mockAppStore()}
        client={client}
        onOpenFile={vi.fn()}
      />
    ));

    await waitFor(() =>
      expect(screen.getByTestId("review-lens-empty").textContent).toContain(
        "Nothing new since you last looked",
      ),
    );

    // Second resolve stays in "resolving" until resolveSecond runs.
    void resolveScope("p1", client, { sessionId: "s1", title: "Focus", sessionScoped: true });
    await waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("review-lens-empty").textContent).toContain(
      "Nothing new since you last looked",
    );
    expect(screen.queryByTestId("review-lens-loading")).toBeNull();

    resolveSecond?.({
      files: [],
      commit_available: false,
      git_changes: [], commands: [], turns: [],
    });
  });

  it("shows the active comparison scope header", async () => {
    render(() => (
      <ReviewLens
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
        onOpenFile={() => {}}
      />
    ));
    await waitFor(() => expect(screen.getByTestId("review-lens")).toBeTruthy());
    expect(screen.getByTestId("review-lens-scope-header").textContent).toContain(
      "New since you looked",
    );
  });

  it("shows empty copy for the new-since-you-looked lens", async () => {
    render(() => (
      <ReviewLens
        projectId="p1"
        client={mockClient()}
        appStore={mockAppStore()}
        onOpenFile={() => {}}
      />
    ));
    await waitFor(() =>
      expect(screen.getByTestId("review-lens-empty").textContent).toContain(
        "Nothing new since you last looked",
      ),
    );
  });

  it("claims nothing for an unresolved scope and holds its surface until the first resolve settles", async () => {
    let resolveWalk: ((value: unknown) => void) | undefined;
    const client = mockClient({
      listProjectSourceWalk: vi.fn().mockImplementation(
        () => new Promise((resolve) => { resolveWalk = resolve; }),
      ),
    });
    const preparation = createPreparation();
    render(() => (
      <PresentationProvider preparation={preparation}>
        <ReviewLens
          projectId="p1"
          client={client}
          appStore={mockAppStore()}
          onOpenFile={() => {}}
        />
      </PresentationProvider>
    ));
    expect(preparation.pending()).toContain("review-lens");
    expect(screen.queryByTestId("review-lens-empty")).toBeNull();
    await waitFor(() => expect(client.listProjectSourceWalk).toHaveBeenCalled());
    expect(screen.getByTestId("review-lens-loading")).toBeTruthy();
    expect(screen.queryByTestId("review-lens-empty")).toBeNull();

    resolveWalk?.({
      files: [],
      commit_available: false,
      git_changes: [], commands: [], turns: [],
      next_cursor: undefined,
    });
    await waitFor(() =>
      expect(screen.getByTestId("review-lens-empty").textContent).toContain(
        "Nothing new since you last looked",
      ),
    );
    expect(screen.queryByTestId("review-lens-loading")).toBeNull();
    expect(preparation.pending()).not.toContain("review-lens");
  });

  it("does not hold its surface while hidden or without a client", () => {
    const hidden = createPreparation();
    render(() => (
      <PresentationProvider preparation={hidden}>
        <ReviewLens
          projectId="p1"
          client={mockClient()}
          appStore={mockAppStore()}
          visible={false}
          onOpenFile={() => {}}
        />
      </PresentationProvider>
    ));
    expect(hidden.pending()).not.toContain("review-lens");
    cleanup();

    const unconnected = createPreparation();
    render(() => (
      <PresentationProvider preparation={unconnected}>
        <ReviewLens
          projectId="p1"
          client={null}
          appStore={mockAppStore()}
          onOpenFile={() => {}}
        />
      </PresentationProvider>
    ));
    expect(unconnected.pending()).not.toContain("review-lens");
    expect(screen.queryByTestId("review-lens-empty")).toBeNull();
  });

  it("a failed first load reports a notice, never the settled empty copy", async () => {
    // The walk that would establish "no recorded changes" never landed.
    const client = mockClient({
      listProjectSourceWalk: vi.fn().mockRejectedValue(new Error("walk failed")),
    });
    render(() => (
      <ReviewLens
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
        onOpenFile={() => {}}
      />
    ));
    await waitFor(() =>
      expect(noticeRows()).toMatchObject([{ code: "files_review_unavailable", message: "walk failed" }]),
    );
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("review-lens-empty")).toBeNull();
  });

  it("skips the changes fetch while comparison is off", async () => {
    const client = mockClient();
    setComparisonOff("p1", true);
    render(() => (
      <ReviewLens
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
        onOpenFile={() => {}}
      />
    ));
    await waitFor(() =>
      expect(screen.getByTestId("review-lens-scope-header").textContent).toBe(
        "Nothing marked",
      ),
    );
    expect(screen.getByTestId("review-lens-empty").textContent).toContain(
      "Marking is off",
    );
    expect(client.listProjectSourceWalk).not.toHaveBeenCalled();

    setComparisonOff("p1", false);
    await waitFor(() =>
      expect(client.listProjectSourceWalk).toHaveBeenCalled(),
    );
  });

  it("stays idle while its resident pane is hidden", async () => {
    const client = mockClient();
    render(() => (
      <ReviewLens
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
        visible={false}
        onOpenFile={() => {}}
      />
    ));
    await Promise.resolve();
    expect(client.listProjectSourceWalk).not.toHaveBeenCalled();
    expect(client.getProjectSourceStorage).not.toHaveBeenCalled();
  });

  it("lists outside-the-app changes with other files", async () => {
    const external = Array.from({ length: 12 }, (_, i) => {
      const path = `.npm-cache/_cacache/index-v5/${i}`;
      return walkFile(
        path,
        [walkEffect(`c${i}`, path, 1, { origin: "external", turn: 0 })],
        {
          changed_since_presented: false,
          unpresented_agent_effects: 0,
          last_at: "2026-07-30T00:00:00Z",
        },
      );
    });
    render(() => (
      <ReviewLens
        projectId="p1"
        client={mockClient({
          listProjectSourceWalk: vi.fn().mockResolvedValue({
            files: external,
            commit_available: false,
            git_changes: [], commands: [], turns: [],
            next_cursor: undefined,
          }),
        })}
        appStore={mockAppStore()}
        onOpenFile={() => {}}
      />
    ));
    await waitFor(() =>
      expect(screen.queryAllByTestId("changes-row-file")).toHaveLength(12),
    );
  });

  it("holds no diffstat lane open when no row can fill it", async () => {
    const external = walkFile(
      "src/app.ts",
      [walkEffect("c1", "src/app.ts", 1, { origin: "external" })],
      {
        changed_since_presented: false,
        unpresented_agent_effects: 0,
        last_at: "2026-07-30T00:00:00Z",
      },
    );
    render(() => (
      <ReviewLens
        projectId="p1"
        client={mockClient({
          listProjectSourceWalk: vi.fn().mockResolvedValue({
            files: [external],
            commit_available: false,
            git_changes: [], commands: [], turns: [],
            next_cursor: undefined,
          }),
        })}
        appStore={mockAppStore()}
        onOpenFile={() => {}}
      />
    ));

    const row = await screen.findByTestId("changes-row-file");
    expect(row.querySelector(".den-review-row__stat")).toBeNull();
  });

  it("opens rows as ordinary files", async () => {
    const onOpenFile = vi.fn();
    render(() => (
      <ReviewLens
        projectId="p1"
        client={mockClient({
          listProjectSourceWalk: vi.fn().mockResolvedValue({
            files: [
              walkFile("src/app.ts", [walkEffect("change-1", "src/app.ts")], {
                last_at: "2026-07-30T00:00:00Z",
              }),
            ],
            commit_available: true,
            git_changes: [], commands: [], turns: [],
            next_cursor: undefined,
          }),
        })}
        appStore={mockAppStore()}
        onOpenFile={onOpenFile}
      />
    ));

    const row = await screen.findByTestId("changes-row-file");

    fireEvent.click(row);
    expect(onOpenFile).toHaveBeenCalledWith(
      "r1",
      "src/app.ts",
      undefined,
      "file-src/app.ts",
    );
  });

  it("replaces Review changes with the current Walk step", async () => {
    const sourceRow = (
      id: string,
      path: string,
      ordinal: number,
      overrides: Partial<SourceWalkEffect> = {},
    ) =>
      walkFile(path, [walkEffect(id, path, ordinal, overrides)], {
        tip: { state: "content", sha256: `${id}-sha` },
      });
    const client = mockClient({
      readComparison: vi.fn().mockResolvedValue({
        in_range: true,
        before: {
          state: "content",
          size_bytes: 4,
          availability: "available",
          content: "one\n",
        },
        after: {
          state: "content",
          size_bytes: 8,
          availability: "available",
          content: "one\ntwo\n",
        },
        location_changed: false,
      }),
      listProjectSourceWalk: vi.fn().mockResolvedValue({
        files: [
          walkFile(
            "src/a.ts",
            [
              walkEffect("c1", "src/a.ts", 1, { tool_name: "edit" }),
              walkEffect("c2", "src/a.ts", 2),
            ],
            { tip: { state: "content", sha256: "c2-sha" } },
          ),
          sourceRow("c3", "src/b.ts", 3),
        ],
        commit_available: true,
        git_changes: [], commands: [], turns: [],
        next_cursor: undefined,
      }),
    });
    render(() => (
      <ReviewLens
        projectId="p1"
        client={client}
        appStore={mockAppStore()}
        onOpenFile={() => undefined}
      />
    ));

    const rows = await screen.findAllByTestId("changes-row-file");
    fireEvent.click(rows[0]!);
    expect(rows[0]!.classList.contains("den-review-row--open")).toBe(true);

    await enterWalk("p1", client, "s1");
    expect(await screen.findByTestId("review-walk-context")).toBeTruthy();
    expect(screen.queryByTestId("review-lens-scope")).toBeNull();
    expect(screen.queryByTestId("review-lens-list")).toBeNull();
    expect(screen.queryByTestId("changes-row-file")).toBeNull();
    expect(screen.getByTestId("review-walk-position").textContent).toBe(
      "1 of 3",
    );
    expect(screen.getByTestId("review-walk-step").textContent).toContain(
      "a.ts",
    );
    expect(screen.getByTestId("review-walk-summary").textContent).toBe(
      "Added 1 line across 1 area.",
    );
    expect(screen.getByTestId("review-walk-path").textContent).toBe(
      "src/a.ts",
    );
    expect(screen.getByTestId("review-walk-stats").textContent.trim()).toBe(
      "+1",
    );
    expect(screen.getByTestId("review-walk-tool").textContent).toBe("edit");
    expect(screen.getByTestId("review-walk-turn").textContent).toBe("1");
    expect(screen.getByTestId("review-walk-line-count").textContent).toBe(
      "1 → 2 lines",
    );
    expect(screen.getByTestId("review-walk-file-position").textContent).toBe(
      "Step 1 of 2",
    );
    expect(screen.getByTestId("review-walk-areas").textContent).toContain(
      "Line 2",
    );

    setWalkAt("p1", 1);
    await waitFor(() => {
      expect(screen.getByTestId("review-walk-position").textContent).toBe(
        "2 of 3",
      );
      expect(screen.getByTestId("review-walk-step").textContent).toContain(
        "a.ts",
      );
      expect(screen.getByTestId("review-walk-file-position").textContent).toBe(
        "Step 2 of 2",
      );
    });

    setWalkAt("p1", 2);
    await waitFor(() => {
      expect(screen.getByTestId("review-walk-position").textContent).toBe(
        "3 of 3",
      );
      expect(screen.getByTestId("review-walk-step").textContent).toContain(
        "b.ts",
      );
    });

    leaveWalk("p1");
    await waitFor(() =>
      expect(screen.getByTestId("review-lens-scope")).toBeTruthy(),
    );
    expect(screen.queryByTestId("review-walk-context")).toBeNull();
    expect(screen.getAllByTestId("changes-row-file")).toHaveLength(2);
  });

  it("opens a retained step inside its ordinary file", async () => {
    const onOpenFile = vi.fn();
    const change = (id: string, path: string, observed_at: string) =>
      walkEffect(id, path, 1, { observed_at, batch_id: "batch-1" });
    render(() => (
      <ReviewLens
        projectId="p1"
        client={mockClient({
          listProjectSourceWalk: vi.fn().mockResolvedValue({
            files: [
              walkFile(
                "src/a.ts",
                [
                  change("new-a", "src/a.ts", "2026-08-09T02:00:00Z"),
                  change("old-a", "src/a.ts", "2026-08-09T01:00:00Z"),
                ],
                { tip: { state: "content", sha256: "new-a" } },
              ),
              walkFile(
                "src/b.ts",
                [
                  change("new-b", "src/b.ts", "2026-08-09T02:00:00Z"),
                  change("old-b", "src/b.ts", "2026-08-09T01:00:00Z"),
                ],
                { tip: { state: "content", sha256: "new-b" } },
              ),
            ],
            commit_available: true,
            git_changes: [], commands: [], turns: [],
            next_cursor: undefined,
          }),
        })}
        appStore={mockAppStore()}
        onOpenFile={onOpenFile}
      />
    ));

    fireEvent.click((await screen.findAllByTestId("changes-row-file"))[0]!);
    fireEvent.click(screen.getByTestId("changes-row-steps-toggle"));
    const steps = screen.getAllByTestId("changes-step");
    fireEvent.click(steps[0]!);

    expect(onOpenFile).toHaveBeenLastCalledWith(
      "r1",
      "src/a.ts",
      expect.objectContaining({ id: "new-a" }),
      "file-src/a.ts",
    );
  });

  describe("seen files", () => {
    const seenFile: SourceSeenFile = {
      file_id: "file-src/seen.ts",
      root_id: "r1",
      path: "src/seen.ts",
      tip: { state: "content", sha256: "seen" },
      seen_at: "2026-09-11T10:00:00Z",
      through_ordinal: 4,
      effects: [walkEffect("seen-1", "src/seen.ts", 4)],
      effects_truncated: false,
    };

    afterEach(() => resetFirstTimeTipRequestsForTests());

    it("opens current text normally and loads the exact reviewed range only on request", async () => {
      const open = vi.fn(), reviewed = vi.fn();
      const compare = vi.fn().mockResolvedValue({
        in_range: true, location_changed: false,
        before: { state: "content", availability: "available", size_bytes: 4, content: "old\n" },
        after: { state: "content", availability: "available", size_bytes: 4, content: "new\n", version_id: "reviewed-version" },
      });
      const complete = vi.fn();
      render(() => <ReviewLens projectId="p1" appStore={mockAppStore()}
        client={mockClient({
          listProjectSourceSeen: vi.fn().mockResolvedValue({ files: [seenFile], next_cursor: undefined }),
          readComparison: compare, completeProjectSourcePresentation: complete,
        })} onOpenFile={open} onViewReviewed={reviewed} />);
      fireEvent.click(await screen.findByTestId("changes-row-file"));
      expect(open).toHaveBeenCalledWith("r1", "src/seen.ts", undefined, seenFile.file_id);
      expect(compare).not.toHaveBeenCalled();
      expect(screen.queryByTestId("changes-row-revert")).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "View reviewed changes" }));
      await waitFor(() => expect(reviewed).toHaveBeenCalledWith(expect.objectContaining({
        reviewedThroughOrdinal: 4, versionId: "reviewed-version",
        initialComparison: "before", source: expect.objectContaining({ kind: "reader" }),
      })));
      expect(compare).toHaveBeenCalledExactlyOnceWith("p1", { fileId: seenFile.file_id, reviewedThroughOrdinal: 4 }, { sessionId: "" });
      expect(complete).not.toHaveBeenCalled();
    });

    it("keeps the current selection and reports a notice when reviewed history fails", async () => {
      const reviewed = vi.fn();
      render(() => <ReviewLens projectId="p1" appStore={mockAppStore()}
        client={mockClient({
          listProjectSourceSeen: vi.fn().mockResolvedValue({ files: [seenFile], next_cursor: undefined }),
          readComparison: vi.fn().mockRejectedValue(new Error("offline")),
        })} onOpenFile={vi.fn()} onViewReviewed={reviewed} />);
      fireEvent.click(await screen.findByTestId("changes-row-file"));
      fireEvent.click(screen.getByRole("button", { name: "View reviewed changes" }));
      await waitFor(() => expect(noticeRows()).toMatchObject([
        { code: "files_reviewed_unavailable", message: "Couldn’t load reviewed changes for seen.ts." },
      ]));
      expect(reviewed).not.toHaveBeenCalled();
      await waitFor(() => expect(screen.getByRole("button", { name: "View reviewed changes" }).hasAttribute("disabled")).toBe(false));
    });

    it("sit below what is new, and marking one unseen withdraws its look", async () => {
      const listProjectSourceSeen = vi
        .fn()
        .mockResolvedValueOnce({ files: [seenFile], next_cursor: "cursor-1" })
        .mockResolvedValue({ files: [], next_cursor: undefined });
      const withdrawProjectSourcePresentation = vi.fn().mockResolvedValue(undefined);
      render(() => (
        <ReviewLens
          projectId="p1"
          appStore={mockAppStore()}
          client={mockClient({ listProjectSourceSeen, withdrawProjectSourcePresentation })}
          onOpenFile={vi.fn()}
        />
      ));

      expect((await screen.findByTestId("review-lens-seen-heading")).textContent).toContain("Seen");
      expect(screen.getByTestId("review-lens-seen-count").textContent).toBe("1+");
      expect(screen.getByTestId("review-lens-empty").textContent).toContain(
        "Nothing new since you last looked",
      );
      expect(requestedFirstTimeTips()).toContain("review-seen");

      fireEvent.click(screen.getByTestId("changes-row-mark-unseen"));

      await waitFor(() =>
        expect(withdrawProjectSourcePresentation).toHaveBeenCalledWith("p1", "file-src/seen.ts", 4),
      );
      await waitFor(() => expect(screen.queryByTestId("review-lens-seen-heading")).toBeNull());
    });

    it("say nothing when a newer look already replaced the one being withdrawn", async () => {
      const withdrawProjectSourcePresentation = vi
        .fn()
        .mockRejectedValue(new LycaonApiError("replaced", 409, "source_presentation_effect_changed"));
      render(() => (
        <ReviewLens
          projectId="p1"
          appStore={mockAppStore()}
          client={mockClient({
            listProjectSourceSeen: vi.fn().mockResolvedValue({ files: [seenFile], next_cursor: undefined }),
            withdrawProjectSourcePresentation,
          })}
          onOpenFile={vi.fn()}
        />
      ));

      fireEvent.click(await screen.findByTestId("changes-row-mark-unseen"));

      await waitFor(() => expect(withdrawProjectSourcePresentation).toHaveBeenCalled());
      expect(noticeRows()).toEqual([]);
    });

    it("ask the host for another page", async () => {
      const listProjectSourceSeen = vi.fn().mockResolvedValue({ files: [seenFile], next_cursor: "cursor-1" });
      const client = mockClient({ listProjectSourceSeen });
      render(() => (
        <ReviewLens projectId="p1" appStore={mockAppStore()} client={client} onOpenFile={vi.fn()} />
      ));

      fireEvent.click(await screen.findByTestId("review-lens-seen-more"));

      await waitFor(() =>
        expect(listProjectSourceSeen).toHaveBeenLastCalledWith(
          "p1",
          expect.objectContaining({ limit: 50 }),
        ),
      );
    });
  });
});

describe("ReviewLens names and lists the answer on screen", () => {
  afterEach(() => {
    cleanup();
    resetFilesStagePaneForTests();
    resetScopeResolutionForTests();
    resetReviewLiveForTests();
    resetWalkForTests();
  });

  const page = (files: SourceWalkFile[], baseline = "presentation") => ({
    baseline, files, commit_available: false, git_changes: [], commands: [], turns: [], next_cursor: undefined,
  });

  it("keeps the old header over the old rows until the picked comparison's rows arrive", async () => {
    let release!: () => void;
    const listProjectSourceWalk = vi.fn(async (_projectId: string, opts: { baseline: string }) => {
      if (opts.baseline === "presentation") return page([walkFile("seen-before.ts", [walkEffect("e1", "seen-before.ts")])]);
      await new Promise<void>((resolve) => { release = resolve; });
      return page([walkFile("this-turn.ts", [walkEffect("e2", "this-turn.ts")])], "turn:s1,2");
    });
    render(() => (
      <ReviewLens projectId="p1" appStore={mockAppStore()} client={mockClient({ listProjectSourceWalk })} onOpenFile={vi.fn()} />
    ));
    await screen.findByText("seen-before.ts");

    chooseSidebarScope("p1", { kind: "turn" });
    await waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledWith("p1", expect.objectContaining({ baseline: "turn:s1" })));
    expect(screen.getByTestId("review-lens-scope-header").textContent).toBe("New since you looked");
    expect(screen.getByText("seen-before.ts")).toBeTruthy();

    release();
    await screen.findByText("this-turn.ts");
    expect(screen.getByTestId("review-lens-scope-header").textContent).toBe("Changes in this turn");
    expect(screen.queryByText("seen-before.ts")).toBeNull();
  });

  it("asks for a chat instead of calling a chat comparison empty", async () => {
    chooseSidebarScope("p1", { kind: "turn" });
    const store = { state: { currentSession: null, messages: [], workers: [] } } as unknown as AppStore;
    const listProjectSourceWalk = vi.fn();
    render(() => (
      <ReviewLens projectId="p1" appStore={store} client={mockClient({ listProjectSourceWalk })} onOpenFile={vi.fn()} />
    ));
    expect((await screen.findByTestId("review-lens-empty")).textContent).toBe("Select a chat to see what it changed.");
    expect(listProjectSourceWalk).not.toHaveBeenCalled();
  });

  it("lists this chat's edit in flight, but not a read or another chat's edit", async () => {
    chooseSidebarScope("p1", { kind: "turn" });
    const listProjectSourceWalk = vi.fn(async () => page([], "turn:s1,3"));
    render(() => (
      <ReviewLens projectId="p1" appStore={mockAppStore()} client={mockClient({ listProjectSourceWalk })} onOpenFile={vi.fn()} />
    ));
    await screen.findByTestId("review-lens-empty");
    const presence = (sessionId: string, over: Partial<AgentSessionPresence>): AgentSessionPresence => ({
      session_id: sessionId, title: sessionId, turn: 3, activities: [], reads: [], intents: [], worker_drafts: [], ...over,
    });
    syncReviewPresence("p1", new Map([
      ["s1", presence("s1", {
        activities: [
          { tool_call_id: "r1", tool: "read", root_id: "r1", path: "read-only.ts", kind: "reading" },
          { tool_call_id: "w1", tool: "write", root_id: "r1", path: "mine.ts", kind: "editing" },
        ],
      })],
      ["s2", presence("s2", {
        activities: [{ tool_call_id: "w2", tool: "write", root_id: "r1", path: "theirs.ts", kind: "editing" }],
      })],
    ]));

    await screen.findByText("mine.ts");
    expect(screen.queryByText("read-only.ts")).toBeNull();
    expect(screen.queryByText("theirs.ts")).toBeNull();
  });

  it("keeps each row's element when a refresh changes other rows", async () => {
    let second = false;
    const listProjectSourceWalk = vi.fn(async () => page([
      walkFile("stable.ts", [walkEffect("e1", "stable.ts")]),
      walkFile("other.ts", [walkEffect(second ? "e3" : "e2", "other.ts", second ? 3 : 2)]),
    ]));
    render(() => (
      <ReviewLens projectId="p1" appStore={mockAppStore()} client={mockClient({ listProjectSourceWalk })} onOpenFile={vi.fn()} />
    ));
    const before = (await screen.findByText("stable.ts")).closest("[data-path]");
    second = true;
    await resolveScope("p1", mockClient({ listProjectSourceWalk }), { sessionId: "s1", title: "Focus", sessionScoped: true });
    await waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledTimes(2));
    expect(screen.getByText("stable.ts").closest("[data-path]")).toBe(before);
  });
});
