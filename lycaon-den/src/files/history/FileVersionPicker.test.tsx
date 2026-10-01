import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { PRESENTATION_LOADING_GRACE_MS } from "../../ui/presentation.ts";
import type {
  SourceFileCommit,
  SourceFileVersion,
  SourceGitChange,
} from "../../api/types.ts";
import { FileVersionPicker } from "./FileVersionPicker.tsx";
import {
  fileVersionTitle,
  type FileVersionHistory,
  type FileVersionView,
} from "./file-version.ts";

function version(id: string, op: SourceFileVersion["op"] = "write"): SourceFileVersion {
  return {
    id,
    file_id: "file-a",
    workspace_kind: "project",
    operation_id: `operation-${id}`,
    effect_id: `effect-${id}`,
    root_id: "r1",
    path: "src/a.ts",
    state: "content",
    content_sha256: `sha-${id}`,
    size_bytes: 1,
    capture_state: "stored",
    op,
    origin: "agent",
    turn: 1,
    ordinal: 2,
    cause: "tool",
    capture_quality: "exact",
    landing: "working_file",
    created_at: "2026-08-23T10:00:00Z",
  };
}

/** An AI edit that landed in the editor while disk had moved under it. */
function heldVersion(id: string): SourceFileVersion {
  const held = version(id);
  return {
    ...held,
    op: undefined,
    effect_id: undefined,
    cause: "agent_edit_held",
    landing: "editor_document",
  };
}

function commit(overrides: Partial<SourceFileCommit> = {}): SourceFileCommit {
  return {
    commit: overrides.commit ?? "a1b2c3f4d5e6",
    subject: overrides.subject ?? "Tighten step bar copy",
    author_name: overrides.author_name ?? "Sam Okafor",
    authored_at: overrides.authored_at ?? "2026-08-22T10:00:00Z",
    committed_at: overrides.committed_at ?? "2026-08-22T10:00:00Z",
    source_path: overrides.source_path ?? "src/a.ts",
    blob_oid: overrides.blob_oid ?? "blob-oid-1",
    matches_version_id: overrides.matches_version_id,
    arrival_git_change_id: overrides.arrival_git_change_id,
  };
}

const checkout: SourceGitChange = {
  session_id: "", turn: 0, tool_call_id: "", tool_name: "",
  id: "t1",
  root_id: "r1",
  kind: "checkout",
  from_ref: "main",
  to_ref: "feature-x",
  ordinal: 4,
  observed_at: "2026-08-23T10:00:00Z",
};

function history(overrides: Partial<FileVersionHistory> = {}): FileVersionHistory {
  return {
    status: "ready",
    gitStatus: "ready",
    current: { state: "content", sha256: "current-sha" },
    versions: [],
    commits: [],
    arrivals: [],
    gitHistoryState: "available",
    trackedSince: null,
    nextVersionsCursor: null,
    nextGitCursor: null,
    loadingMore: false,
    ...overrides,
  };
}

function selected(): FileVersionView {
  return { versionId: "c2",
fileId: "file-a",
rootId: "r1",
path: "src/a.ts",
op: "write",
ts: "2026-08-23T10:00:00Z",
beforeAvailability: "available",
sha256: "abc",
sizeBytes: 8,
availability: "available", source: { kind: "text", before: "before\n", after: "changed\n" } };
}

function mount(props: Partial<Parameters<typeof FileVersionPicker>[0]> = {}) {
  const handlers = {
    selected: null,
    history: history(),
    onCurrent: vi.fn(),
    onSelect: vi.fn(),
    onSelectCommit: vi.fn(),
    onRestoreVersion: vi.fn(),
    onRestoreCommit: vi.fn(),
    restoreReason: () => null,
    onOpen: vi.fn(),
    onLoadMore: vi.fn(),
    ...props,
  } as Parameters<typeof FileVersionPicker>[0];
  render(() => <FileVersionPicker {...handlers} />);
  return handlers;
}

describe("FileVersionPicker", () => {
  afterEach(() => { cleanup(); vi.useRealTimers(); });

  it("marks an AI edit the working file never received", () => {
    mount({ history: history({ versions: [heldVersion("c9")] }) });
    fireEvent.click(screen.getByTestId("file-version-trigger"));

    expect(screen.getByText("AI · Never saved to disk")).toBeTruthy();
    expect(screen.getByText(fileVersionTitle(heldVersion("c9")))).toBeTruthy();
  });

  it("selects a retained version or the working file", () => {
    const older = version("c1", "create");
    const current = version("c2");
    const props = mount({
      selected: selected(),
      history: history({
        versions: [current, older],
        nextVersionsCursor: "versions-page-2",
      }),
    });

    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(props.onOpen).toHaveBeenCalledOnce();
    expect(
      screen.getByRole("menuitemradio", { name: /Edited/ }).getAttribute(
        "aria-checked",
      ),
    ).toBe("true");
    fireEvent.click(screen.getByRole("menuitemradio", { name: /Created/ }));
    expect(props.onSelect).toHaveBeenCalledWith(older);

    fireEvent.click(screen.getByTestId("file-version-trigger"));
    fireEvent.click(screen.getByTestId("file-version-current"));
    expect(props.onCurrent).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByTestId("file-version-trigger"));
    fireEvent.click(screen.getByRole("button", { name: "Load earlier history" }));
    expect(props.onLoadMore).toHaveBeenCalledOnce();
  });

  it("describes Current as the deletion when no working file exists", () => {
    mount({ currentAbsent: true, history: history({ versions: [version("c3", "delete")] }) });
    fireEvent.click(screen.getByTestId("file-version-trigger"));

    const current = screen.getByTestId("file-version-current");
    expect(current.textContent).toContain("Deleted · no working file");
    expect(current.textContent).not.toContain("Editable working file");
    expect(current.getAttribute("aria-checked")).toBe("true");
  });

  it("asks for a fresh history on every open, so recent versions appear", () => {
    const props = mount({ history: history({ versions: [version("v1")] }) });
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(props.onOpen).toHaveBeenCalledTimes(2);
  });

  it("titles a git-caused version by its movement and where it went", () => {
    const caused = version("v-git");
    caused.origin = "external";
    caused.actor_label = "Outside app";
    caused.git_change = checkout;
    mount({ history: history({ versions: [caused] }) });

    fireEvent.click(screen.getByTestId("file-version-trigger"));
    const row = screen.getByRole("menuitemradio", { name: /Git checkout/ });
    expect(row.textContent).toContain("Git checkout");
    expect(row.textContent).toContain("main → feature-x");
    expect(row.textContent).not.toContain("Outside app");
  });

  it("names a retained state that no effect produced, and its branch", () => {
    const baseline = version("v-base");
    delete baseline.op;
    delete baseline.origin;
    delete baseline.effect_id;
    delete baseline.operation_id;
    const worker = version("v-worker");
    worker.workspace_kind = "worker";

    mount({ history: history({ versions: [worker, baseline] }) });
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(screen.getByRole("menuitemradio", { name: /Earlier state/ }))
      .toBeTruthy();
    expect(screen.getByRole("menuitemradio", { name: /Worker branch/ }))
      .toBeTruthy();
  });

  it("carries both identities on a save whose bytes were committed", () => {
    const saved = version("v-saved");
    mount({
      history: history({
        versions: [saved],
        gitHistoryState: "available",
        commits: [commit({ matches_version_id: "v-saved" })],
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));

    const rows = screen.getAllByRole("menuitemradio");
    const versionRow = rows.find((row) => row.textContent?.includes("Edited"));
    expect(versionRow?.textContent).toContain("committed");
    expect(versionRow?.textContent).toContain("a1b2c3f");
    expect(versionRow?.textContent).toContain("Tighten step bar copy");
    expect(screen.queryByTestId("file-version-commit")).toBeNull();
  });

  it.each([
    "git_commit_restore",
    "version_restore",
  ])("labels %s provenance without claiming a new commit", (cause) => {
    const restored = version("v-restored");
    restored.cause = cause;
    restored.origin = "user";
    mount({
      history: history({
        versions: [restored],
        commits: [commit({ matches_version_id: "v-restored" })],
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));

    const row = screen.getByRole("menuitemradio", { name: /Restored/ });
    expect(row.textContent).toContain("matches commit");
    expect(row.textContent).not.toContain("committed");
    expect(row.textContent).toContain("a1b2c3f");
  });

  it("groups foreign commits under the movement that brought them", () => {
    const props = mount({
      history: history({
        gitHistoryState: "available",
        arrivals: [{ ...checkout, id: "t-pull", kind: "pull", to_ref: "main" }],
        commits: [
          commit({
            commit: "ffff1111",
            subject: "Fix parser edge case",
            arrival_git_change_id: "t-pull",
          }),
        ],
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));

    expect(screen.getByTestId("file-version-arrival").textContent)
      .toContain("Arrived with git pull");
    const row = screen.getByTestId("file-version-commit");
    expect(row.textContent).toContain("Committed");
    expect(row.textContent).toContain("Sam Okafor");
    expect(row.textContent).toContain("Fix parser edge case");

    fireEvent.click(row);
    expect(props.onSelectCommit).toHaveBeenCalledOnce();
  });

  it("marks where tracking began above history that only git holds", () => {
    mount({
      history: history({
        versions: [version("v1")],
        gitHistoryState: "available",
        trackedSince: "2026-08-26T19:24:00Z",
        commits: [commit({ commit: "8f21d0cabc", subject: "Extract rail pitch helper" })],
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));

    const boundary = screen.getByTestId("file-version-boundary");
    expect(boundary.textContent).toContain("Tracked since");
    const commitRow = screen.getByTestId("file-version-commit");
    expect(
      boundary.compareDocumentPosition(commitRow) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("restores a listed row in one click, and hides the action when refused", () => {
    const props = mount({
      history: history({
        versions: [version("v1")],
        gitHistoryState: "available",
        commits: [commit({ commit: "cccc2222" })],
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    const actions = screen.getAllByTestId("file-version-restore-row");
    expect(actions).toHaveLength(2);
    fireEvent.click(actions[0]!);
    expect(props.onRestoreVersion).toHaveBeenCalledOnce();
    expect(props.onSelect).not.toHaveBeenCalled();

    cleanup();
    mount({
      history: history({ versions: [version("v1")] }),
      restoreReason: () => "Save or discard the current draft before restoring.",
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(screen.queryByTestId("file-version-restore-row")).toBeNull();
  });

  it("shows retained versions before the Git history loader", () => {
    vi.useFakeTimers();
    const localVersion = {
      ...version("local-1"),
      created_at: "2026-08-23T09:00:00Z",
    };
    mount({
      history: history({
        gitStatus: "loading",
        versions: [localVersion],
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(screen.getByText(fileVersionTitle(localVersion))).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS);
    expect(screen.getByRole("status", { name: "Loading Git history" }).textContent)
      .toBe("Loading Git history…");
    expect(screen.queryByText("No earlier versions")).toBeNull();
  });

  it("reports initial history loading and failure states", () => {
    vi.useFakeTimers();
    const { unmount } = render(() => (
      <FileVersionPicker
        selected={null}
        history={history({ status: "loading", current: null })}
        onCurrent={vi.fn()}
        onSelect={vi.fn()}
        onSelectCommit={vi.fn()}
        onRestoreVersion={vi.fn()}
        onRestoreCommit={vi.fn()}
        restoreReason={() => null}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(screen.queryByRole("status")).toBeNull();
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS);
    expect(screen.getByRole("status").textContent).toBe("Loading history…");
    unmount();

    mount({
      history: history({
        status: "error",
        current: null,
      }),
    });
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByText("No earlier versions")).toBeNull();
  });

  it("keeps one waiting interval across history phases and resets it on close", () => {
    vi.useFakeTimers();
    const [value, setValue] = createSignal(history({ status: "loading", gitStatus: "loading" }));
    render(() => <FileVersionPicker selected={null} history={value()}
      onCurrent={vi.fn()} onSelect={vi.fn()} onSelectCommit={vi.fn()}
      onRestoreVersion={vi.fn()} onRestoreCommit={vi.fn()} restoreReason={() => null}
      onOpen={vi.fn()} onLoadMore={vi.fn()} />);
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS - 1);
    expect(screen.queryByRole("status")).toBeNull();
    setValue(history({ gitStatus: "loading" }));
    vi.advanceTimersByTime(1);
    expect(screen.getByRole("status", { name: "Loading Git history" })).toBeTruthy();
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    fireEvent.click(screen.getByTestId("file-version-trigger"));
    expect(screen.queryByRole("status")).toBeNull();
    setValue(history());
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS);
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.getByText("No earlier versions")).toBeTruthy();
  });

  it.each(["timed_out" as const, "failed" as const])("does not claim no history when the Git lane %s", (state) => {
    mount({ history: history({ gitHistoryState: state }) });

    fireEvent.click(screen.getByTestId("file-version-trigger"));

    expect(screen.queryByText("No earlier versions")).toBeNull();
  });

  it("still says no earlier versions once the lane has actually answered", () => {
    mount({ history: history({ gitHistoryState: "available" }) });

    fireEvent.click(screen.getByTestId("file-version-trigger"));

    expect(screen.getByText("No earlier versions")).toBeTruthy();
  });

  it("stays silent when there is no repository to ask", () => {
    mount({ history: history({ gitHistoryState: "no_repository" }) });

    fireEvent.click(screen.getByTestId("file-version-trigger"));

    expect(screen.getByText("No earlier versions")).toBeTruthy();
  });
});
