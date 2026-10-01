import "../../test/document-outbox-fixture.ts";
import { required } from "../../test/at.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { PROJECT, ROOTS, loadedBuffer, editorDocumentFixtureFor, resetProjectFilesViewTest } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectSourceReadResponse } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { createAppStore } from "../../store/app-state.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { applyBufferSourceChanged } from "../documents/buffer-live.ts";
import { editorReplica, resolveEditorDocument } from "../documents/editor-document.ts";
import { applyFilesBufferDraft, applyFilesBufferEditorDocument, applyFilesBufferLoad, applyFilesBufferUnsupportedEncoding, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

const PATH = "crates/hello_registry/Cargo.toml";
function source(overrides: Partial<ProjectSourceReadResponse> = {}): ProjectSourceReadResponse {
  return {
    workspace_id: "workspace-1", workspace_kind: "project", root_id: "r1",
    file_id: "", version_id: "", path: PATH, content: "[package]\n",
    binary: false, over_limit: false, writable: true, encoding: "utf-8",
    mime: "text/plain", size_bytes: 10, sha256: "new-sha", ...overrides,
  };
}

function changed() {
  applyBufferSourceChanged(PROJECT, "project", {
    root_id: "r1", path: PATH, op: "write", origin: "external", changed_at: "2026-09-02T00:00:00Z",
  });
}

async function mount(
  getProjectSource: LycaonClient["getProjectSource"],
  overrides: Partial<LycaonClient> = {},
) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  const client = { ...getLycaonClient()!,
    observeEditorDocument: vi.fn(async () => {
      const host = editorDocumentFixtureFor(PROJECT, "r1", PATH);
      host.wire.diverged = true;
      host.wire.revision++;
      return host.wire;
    }),
    ...overrides, getProjectSource };
  render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />);
  await waitFor(() => expect(screen.getByTestId("project-files-stage-boundary").dataset.ready).toBe("true"), { timeout: 5000 });
  return client;
}

describe("File classification lifecycle", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => registerNoticePublisher(null));

  it.each(["unsupported", "binary", "oversized"] as const)("reclassifies %s content with a retained live replica", async (kind) => {
    const buf = loadedBuffer({ path: PATH });
    const getSource = vi.fn(async () => source());
    await mount(getSource);
    await resolveEditorDocument(PROJECT, buf);
    const current = projectFilesState(PROJECT).byKey[buf.key]!;
    await waitFor(() => expect(editorReplica(current.documentId)).toBeDefined());
    if (kind === "unsupported") applyFilesBufferUnsupportedEncoding(PROJECT, buf.key, "unknown");
    else applyFilesBufferLoad(PROJECT, buf.key, source({
      content: "", binary: kind === "binary", over_limit: kind === "oversized",
      size_bytes: kind === "oversized" ? 5_000_000 : 4,
    }));
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.kind).toBe("text"));
    expect(screen.queryByTestId("files-info-card")).toBeNull();
    expect(screen.getByTestId("files-editor-host")).toBeTruthy();
  });

  it("reclassifies an unsupported preview that retains its host document", async () => {
    const buf = loadedBuffer({ path: PATH });
    applyFilesBufferEditorDocument(PROJECT, buf.key, {
      fileId: "", documentId: "retained-document", revision: 1,
      diverged: false, absent: false, heldAgentVersionId: null, encoding: "utf-8",
      localGeneration: 1, dirty: false, baseSha256: "base-sha", sizeBytes: 4,
      eol: "lf", baseEol: "lf", mixedEol: false, baseMixedEol: false,
    });
    applyFilesBufferUnsupportedEncoding(PROJECT, buf.key, "unknown");
    const getSource = vi.fn(async () => source());
    await mount(getSource);
    const readsBeforeChange = getSource.mock.calls.length;
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.kind).toBe("text"));
    expect(getSource).toHaveBeenCalledTimes(readsBeforeChange + 1);
  });

  it.each(["binary", "oversized", "unsupported", "image"] as const)("reopens %s content as text after a disk write", async (kind) => {
    const buf = loadedBuffer({ path: PATH, kind: kind === "image" ? "image" : "info" });
    if (kind === "oversized") applyFilesBufferLoad(PROJECT, buf.key, source({ over_limit: true, size_bytes: 5_000_000, content: "" }));
    if (kind === "unsupported") applyFilesBufferUnsupportedEncoding(PROJECT, buf.key, "unknown");
    const getSource = vi.fn(async (_projectId: string, _path: string) => source());
    await mount(getSource);
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.kind).toBe("text"));
    expect(getSource.mock.calls.filter(([, path]) => path === PATH)).toHaveLength(1);
    expect(screen.getByTestId("files-editor-host")).toBeTruthy();
    await waitFor(() => {
      expect(screen.queryByTestId("files-info-card")).toBeNull();
      expect(screen.queryByTestId("files-image-viewer")).toBeNull();
    });
  });

  it("recovers from an encoding refusal on the initial read", async () => {
    const key = openFilesBuffer(PROJECT, { rootId: "r1", rootLabel: "repo", path: PATH, intent: "permanent" });
    const getSource = vi.fn()
      .mockRejectedValueOnce(new LycaonApiError("Unsupported encoding", 415, "unsupported_encoding"))
      .mockResolvedValue(source());
    await mount(getSource);
    await waitFor(() => expect(screen.getByTestId("files-info-explain").textContent).toContain("unsupported"));
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[key]?.kind).toBe("text"));
  });

  it("updates text to binary and back without reopening the tab", async () => {
    const buf = loadedBuffer({ path: PATH });
    let current = source({ binary: true, content: "", mime: "application/octet-stream", encoding: undefined, sha256: undefined });
    const getSource = vi.fn(async () => current);
    await mount(getSource);
    changed();
    await waitFor(() => expect(screen.getByTestId("files-info-explain").textContent).toContain("binary"));
    current = source();
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.kind).toBe("text"));
    expect(screen.getByTestId("files-editor-host")).toBeTruthy();
  });

  it("reports encoding errors on refresh and recovers after conversion", async () => {
    const buf = loadedBuffer({ path: PATH });
    let converted = false;
    const getSource = vi.fn(async () => {
      if (!converted) throw new LycaonApiError("Unsupported encoding", 415, "unsupported_encoding");
      return source();
    });
    await mount(getSource);
    changed();
    await waitFor(() => expect(screen.getByTestId("files-info-explain").textContent).toContain("unsupported"));
    expect(projectFilesState(PROJECT).byKey[buf.key]?.encoding).toBeNull();
    converted = true;
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.kind).toBe("text"));
  });

  it.each(["utf-16le", "utf-16be"] as const)("retains an explicit %s choice during refresh", async (encoding) => {
    const buf = loadedBuffer({ path: PATH });
    applyFilesBufferLoad(PROJECT, buf.key, source({ encoding, sha256: "old-sha" }));
    const getSource = vi.fn()
      .mockResolvedValueOnce(source({ binary: true, content: "", encoding: undefined }))
      .mockResolvedValueOnce(source({ encoding }));
    const host = editorDocumentFixtureFor(PROJECT, "r1", PATH);
    host.wire.encoding = encoding;
    host.wire.base_sha256 = "new-sha";
    host.wire.base_content = "[package]\n";
    host.replace("[package]\n");
    await mount(getSource);
    changed();
    await waitFor(() => expect(getSource).toHaveBeenCalledWith(PROJECT, PATH, {
      rootId: "r1", decodeAs: encoding, includeDeleted: true,
    }));
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.baseSha256).toBe("new-sha"));
    expect(projectFilesState(PROJECT).byKey[buf.key]?.encoding).toBe(encoding);
  });

  it.each([false, true])("preserves edits made while a refresh is in flight (failure=%s)", async (fails) => {
    const buf = loadedBuffer({ path: PATH });
    let resolve!: (value: ProjectSourceReadResponse) => void;
    let reject!: (error: Error) => void;
    const response = new Promise<ProjectSourceReadResponse>((yes, no) => { resolve = yes; reject = no; });
    let held = false;
    const getSource = vi.fn(async () => held ? response : source());
    await mount(getSource);
    await waitFor(() => expect(editorReplica(required(projectFilesState(PROJECT).byKey[buf.key]).documentId)).toBeDefined());
    const readsBeforeChange = getSource.mock.calls.length;
    held = true;
    changed();
    await waitFor(() => expect(getSource).toHaveBeenCalledTimes(readsBeforeChange + 1));
    required(editorReplica(required(projectFilesState(PROJECT).byKey[buf.key]).documentId)).replaceLocal("unsaved edits");
    applyFilesBufferDraft(PROJECT, buf.key, "unsaved edits");
    if (fails) reject(new LycaonApiError("Unsupported encoding", 415, "unsupported_encoding"));
    else resolve(source({ binary: true, content: "", mime: "application/octet-stream" }));
    await response.catch(() => undefined);
    await Promise.resolve();
    expect(projectFilesState(PROJECT).byKey[buf.key]).toMatchObject({ kind: "text", dirty: true });
    expect(filesBufferText(required(projectFilesState(PROJECT).byKey[buf.key]))).toEqual("unsaved edits");
  });

  it("reports a failed explicit encoding reopen as a notice", async () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    loadedBuffer({ path: PATH, kind: "info" });
    const getSource = vi.fn().mockRejectedValueOnce(new LycaonApiError("File is unavailable", 404, "source_not_found"));
    await mount(getSource);
    fireEvent.click(screen.getByTestId("files-info-open-text-btn"));
    fireEvent.click(await screen.findByTestId("files-info-open-utf16le-menu"));
    await waitFor(() => {
      const rows = selectProjectNoticeGroups(store.index()).find(group => group.projectId === PROJECT)?.notices ?? [];
      expect(rows.length).toBeGreaterThan(0);
    });
    expect(screen.queryByTestId("files-info-explain")).toBeNull();
  });

  it("ignores a superseded refresh reporting another workspace", async () => {
    const buf = loadedBuffer({ path: PATH, kind: "info" });
    let resolveStale!: (value: ProjectSourceReadResponse) => void;
    const stale = new Promise<ProjectSourceReadResponse>((resolve) => { resolveStale = resolve; });
    const getSource = vi.fn().mockReturnValueOnce(stale).mockResolvedValueOnce(source());
    await mount(getSource);
    changed();
    changed();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buf.key]?.kind).toBe("text"));
    resolveStale(source({ workspace_id: "retired-workspace" }));
    await stale;
    await Promise.resolve();
    expect(screen.getByTestId("files-editor-host")).toBeTruthy();
  });

  it("presents an info card when opening a binary file with condenseContext requested", async () => {
    const binaryPath = "bin/webdemo.db-wal";
    const key = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: binaryPath,
      intent: "transient",
      condenseContext: true,
    });
    const getSource = vi.fn(async () => source({
      path: binaryPath,
      binary: true,
      content: "",
      mime: "application/octet-stream",
      encoding: undefined,
      sha256: undefined,
    }));
    await mount(getSource);
    await waitFor(() => expect(screen.getByTestId("files-info-card")).toBeTruthy());
    expect(screen.getByTestId("files-info-explain").textContent).toContain("binary");
    expect(projectFilesState(PROJECT).byKey[key]?.condenseContext).toBe(false);
    expect(screen.getByTestId("project-files-stage-boundary").dataset.presentation).toBe("published");
  });
});

