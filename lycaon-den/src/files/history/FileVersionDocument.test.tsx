import { render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal, type Setter } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { FileVersionDocument } from "./FileVersionDocument.tsx";
import { fileVersionFromSnapshot, type FileVersionView } from "./file-version.ts";
import { openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import type { ContentNotice } from "../source/source-content-availability.ts";
import { stubClient } from "../../test/client-fixture.ts";

vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "view-main", prepareClientIdentity: async () => "view-main" }));
beforeEach(resetProjectFilesForTests);

it("announces each historical selection after its requested rows are ready", async () => {
  const key = openFilesBuffer("p1", { rootId: "r1", rootLabel: "repo", path: "a.ts", intent: "permanent" });
  const buffer = projectFilesState("p1").byKey[key]!;
  const host=sourceReaderFixture();
  const client=stubClient(host.methods);
  const snapshot = (content: string) => fileVersionFromSnapshot({ rootId: "r1", path: "a.ts", before: "", after: content });
  let select!: Setter<FileVersionView>;
  const ready=vi.fn();
  render(() => {
    const [version, setVersion] = createSignal(snapshot("first version\n")); select=setVersion;
    return <FileVersionDocument client={client} projectId="p1" buffer={buffer} version={version} preview comparison={() => "before"}
      currentContent={() => null} comparisonMenuOpen={() => false} onOpenComparisonMenu={() => {}} onCurrent={() => {}} onReady={ready} />;
  });
  await waitFor(()=>expect(screen.getByText(/first version/)).toBeTruthy());
  await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
  select(snapshot("selected version\n"));
  expect(ready).toHaveBeenLastCalledWith(false);
  await waitFor(()=>expect(screen.getByText(/selected version/)).toBeTruthy());
  await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
  expect(screen.queryByText(/first version/)).toBeNull();
  expect(document.querySelector('.cm-editor')).not.toBeNull();
});

it("compares the host's current path after a rename without reading the frontend body", async () => {
  const key = openFilesBuffer("p1", { rootId: "r1", rootLabel: "repo", path: "renamed.ts", intent: "permanent" });
  const buffer = projectFilesState("p1").byKey[key]!;
  const host = sourceReaderFixture();
  const reader = host.prepare("old\n", "retained\n", "original.ts");
  const version: FileVersionView = { ...fileVersionFromSnapshot({ rootId: "old-root", path: "original.ts", before: "old\n", after: "retained\n" }), source: { kind: "reader", reader } };
  const open = vi.fn(host.createSourceView);
  const currentContent = vi.fn(() => "full frontend content");
  const client = stubClient({ ...host.methods, createSourceView: open });
  render(() => <FileVersionDocument client={client} projectId="p1" buffer={buffer} version={() => version} comparison={() => "current"}
    currentContent={currentContent} comparisonMenuOpen={() => false} onOpenComparisonMenu={() => {}} onCurrent={() => {}} />);
  await waitFor(() => expect(open).toHaveBeenCalled());
  expect(open.mock.calls[0]?.[1]).toMatchObject({ source: { kind: "retained", view_id: reader.view.id, comparison: "current", root_id: "r1", path: "renamed.ts" } });
  expect(open.mock.calls[0]?.[1]).not.toHaveProperty("before");
  expect(currentContent).not.toHaveBeenCalled();
});

for (const outcome of ["success", "failure", "superseded"] as const) {
  it(`retains the published version through a delayed ${outcome}`, async () => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    const key = openFilesBuffer("p1", { rootId: "r1", rootLabel: "repo", path: "a.ts", intent: "permanent" });
    const buffer = projectFilesState("p1").byKey[key]!;
    const host = sourceReaderFixture();
    let release!: () => void;
    const held = new Promise<void>(resolve => { release = resolve; });
    let hold = false;
    const rows = vi.fn(async (...args: Parameters<typeof host.getSourceViewRows>) => {
      if (hold) {
        hold = false;
        await held;
        if (outcome === "failure") throw new Error("Historical content unavailable");
      }
      return host.getSourceViewRows(...args);
    });
    const client = stubClient({ ...host.methods, getSourceViewRows: rows });
    const snapshot = (after: string) => fileVersionFromSnapshot({ rootId: "r1", path: "a.ts", before: "", after });
    let select!: Setter<FileVersionView>;
    const ready = vi.fn();
    render(() => {
      const [version, setVersion] = createSignal(snapshot("original contents\n"));
      select = setVersion;
      return <FileVersionDocument client={client} projectId="p1" buffer={buffer} version={version} preview comparison={() => "before"}
        currentContent={() => null} comparisonMenuOpen={() => false} onOpenComparisonMenu={() => {}} onCurrent={() => {}} onReady={ready} />;
    });
    await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
    const original = screen.getByText(/original contents/);
    const outgoing = original.closest<HTMLElement>("[data-resident-key]")!;
    hold = true;
    select(snapshot("delayed contents\n"));
    await waitFor(() => expect(rows).toHaveBeenCalledTimes(2));
    expect(original.isConnected).toBe(true);
    expect(outgoing.dataset.resident).toBe("active");
    expect(outgoing.dataset.presentation).toBe("published");
    expect(outgoing.inert).toBe(true);
    expect(ready).toHaveBeenLastCalledWith(false);
    if (outcome === "superseded") {
      select(snapshot("latest contents\n"));
      await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
    }
    release();
    await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
    await waitFor(() => expect(original.isConnected).toBe(false));
    if (outcome === "failure") {
      await waitFor(() => expect(
        selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "p1")?.notices.map(notice => notice.message),
      ).toEqual(["Historical content unavailable"]));
      expect(screen.queryByRole("alert")).toBeNull();
    }
    else expect(screen.getByText(outcome === "superseded" ? /latest contents/ : /delayed contents/)).toBeTruthy();
    if (outcome === "superseded") expect(screen.queryByText(/delayed contents/)).toBeNull();
  });
}

it("keeps an equivalent historical resource mounted when its projection is rebuilt", async () => {
  const key = openFilesBuffer("p1", { rootId: "r1", rootLabel: "repo", path: "a.ts", intent: "permanent" });
  const buffer = projectFilesState("p1").byKey[key]!;
  const host = sourceReaderFixture();
  const rows = vi.fn(host.getSourceViewRows);
  const client = stubClient({ ...host.methods, getSourceViewRows: rows });
  const initial = fileVersionFromSnapshot({ rootId: "r1", path: "a.ts", before: "", after: "retained contents\n" });
  const [version, setVersion] = createSignal(initial);
  const ready = vi.fn();
  render(() => <FileVersionDocument client={client} projectId="p1" buffer={buffer} version={version} preview comparison={() => "before"}
    currentContent={() => null} comparisonMenuOpen={() => false} onOpenComparisonMenu={() => {}} onCurrent={() => {}} onReady={ready} />);
  await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
  const editor = screen.getByText(/retained contents/).closest(".cm-editor");
  const reads = rows.mock.calls.length;
  ready.mockClear();
  setVersion({ ...initial, source: { ...initial.source } });
  expect(ready).toHaveBeenLastCalledWith(true);
  expect(screen.getByText(/retained contents/).closest(".cm-editor")).toBe(editor);
  expect(rows).toHaveBeenCalledTimes(reads);
});

afterEach(() => registerNoticePublisher(null));

// Expected size and type boundaries are calm status; a side the host could
// not produce, or a lost connection, is an alert.
describe("unavailable comparison notices", () => {
  const unavailableVersion = (sides: Partial<FileVersionView>): FileVersionView => ({
    source: { kind: "unavailable" }, versionId: "v1", fileId: "f1", rootId: "r1", path: "big.txt", op: "write",
    ts: "2026-09-24T10:00:00Z", beforeAvailability: "available", availability: "available", sha256: null, sizeBytes: 100,
    ...sides,
  });
  const cases: { name: string; version: Partial<FileVersionView>; comparison: "current" | "before"; notice?: ContentNotice; connected?: boolean; role: "status" | "alert"; text: string }[] = [
    { name: "a version too large to keep", version: { availability: "not_captured", reason: "content_too_large", sizeBytes: 5 * 1024 * 1024 }, comparison: "current", role: "status", text: "5.0 MB" },
    { name: "a binary version", version: { availability: "binary" }, comparison: "current", role: "status", text: "binary" },
    { name: "a before side too large to keep", version: { beforeAvailability: "not_captured", beforeReason: "content_too_large", beforeSizeBytes: 6 * 1024 * 1024 }, comparison: "before", role: "status", text: "6.0 MB" },
    { name: "an unavailable after side while comparing with before", version: { availability: "unavailable", beforeAvailability: "available" }, comparison: "before", role: "alert", text: "unavailable" },
    { name: "an unresolved version", version: { availability: "unresolved" }, comparison: "current", role: "alert", text: "unresolved" },
    { name: "a committed file too large to preview", version: {}, comparison: "before", notice: { tone: "info", message: "This committed file is too large for the text preview." }, role: "status", text: "too large" },
    { name: "a lost connection", version: {}, comparison: "current", connected: false, role: "alert", text: "Reconnect" },
  ];
  for (const tc of cases) {
    it(`presents ${tc.name} as ${tc.role}`, async () => {
      const key = openFilesBuffer("p1", { rootId: "r1", rootLabel: "repo", path: "big.txt", intent: "permanent" });
      const version = unavailableVersion(tc.version);
      const stored = projectFilesState("p1").byKey[key]!;
      const buffer = tc.notice ? { ...stored, diffPreview: { version, notice: tc.notice } } : stored;
      const client = tc.connected === false ? null : stubClient(sourceReaderFixture().methods);
      render(() => <FileVersionDocument client={client} projectId="p1" buffer={buffer} version={() => version} comparison={() => tc.comparison}
        currentContent={() => null} comparisonMenuOpen={() => false} onOpenComparisonMenu={() => {}} onCurrent={() => {}} />);
      const notice = await screen.findByRole(tc.role);
      expect(notice.textContent).toContain(tc.text);
      expect(notice.classList.contains("den-file-version__state--error")).toBe(tc.role === "alert");
      expect(screen.queryByRole(tc.role === "alert" ? "status" : "alert")).toBeNull();
    });
  }
});
