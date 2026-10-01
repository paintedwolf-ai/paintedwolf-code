import { required } from "../../test/at.ts";
import { filesBufferText, filesBufferBase } from "./project-files-buffers.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createMemo, createRoot } from "solid-js";
import { applyFilesBufferDraft, applyFilesBufferEditorDocument, applyFilesBufferLoad, applyFilesBufferSaved, applyFilesBufferUnsupportedEncoding, closeFilesBuffer, closeFilesBuffers, discardFilesBufferDraft, dirtyFilesBuffersUnderPath, dropFilesBuffersForRoot, dropFilesBuffersForProject, filesCloseTargetKeys, markFilesBufferLoading, markFilesBufferTyping, moveFilesBuffer, openFilesBuffer, presentFilesBufferVersion, promoteFilesBuffer, resetProjectFilesForTests, retargetFilesBufferStoreUnderPath, setFilesActiveBuffer, setFilesBufferEol, setFilesBufferPinned, toggleFilesBufferPinned, focusedFilesBufferKey, applyFilesBufferLoadError } from "./project-files-buffers.ts";
import { projectFilesState } from "./files-buffer-state.ts";
import { bufferKindFromSourceRead } from "./project-files-buffer-kind.ts";
import { fileBufferKey } from "../components/project-files-model.ts";
import { scheduleFilesDraftSync, flushFilesDraftSyncFor } from "./files-draft-sync.ts";

const PROJECT = "p1";

function openFixture(path: string) {
  return openFilesBuffer(PROJECT, {
    intent: "permanent",
    rootId: "r1",
    rootLabel: "repo",
    path,
  });
}

function loadedResponse(content: string) {
  return {
    file_id: "",
    version_id: "",
    workspace_id: "workspace-1",
    workspace_kind: "project" as const,
    path: "irrelevant",
    content,
    over_limit: false,
    writable: true,
    binary: false,
    size_bytes: content.length,
    sha256: "abc123",
  };
}

describe("project-files-buffers", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
  });

  it("replaces host language metadata on each source read", () => {
    const key = openFixture("orchard.ts");
    applyFilesBufferLoad(PROJECT, key, { ...loadedResponse("text"), path: "orchard.ts", language: "typescript" });
    expect(projectFilesState(PROJECT).byKey[key]?.language).toEqual({ path: "orchard.ts", name: "typescript" });
    applyFilesBufferLoad(PROJECT, key, { ...loadedResponse("text"), path: "orchard.ts" });
    expect(projectFilesState(PROJECT).byKey[key]?.language).toBeUndefined();
  });

  it.each(["utf-8", "utf-8-bom", "utf-16le", "utf-16le-bom", "utf-16be", "utf-16be-bom"] as const)("keeps authoritative encoding %s when document metadata changes", (encoding) => {
    const key = openFixture("encoding.txt");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("text"));
    const document = {
      fileId: "file", documentId: "document", revision: 2,
      diverged: false, absent: false, heldAgentVersionId: null, encoding,
      localGeneration: 1, dirty: false, baseSha256: "sha", sizeBytes: 4,
      eol: "lf" as const, baseEol: "lf" as const, mixedEol: false, baseMixedEol: false,
    };
    applyFilesBufferEditorDocument(PROJECT, key, document);
    expect(projectFilesState(PROJECT).byKey[key]?.encoding).toBe(encoding);
    applyFilesBufferEditorDocument(PROJECT, key, { ...document, revision: 3, encoding: "utf-8" });
    expect(projectFilesState(PROJECT).byKey[key]?.encoding).toBe("utf-8");
  });

  describe("aim provenance", () => {
    const present = (path: string, origin: "reader" | "presentation") =>
      openFilesBuffer(PROJECT, {
        intent: "permanent",
        rootId: "r1",
        rootLabel: "repo",
        path,
        origin,
      });

    it("keeps a reader's open that is still loading over a presentation", () => {
      const asked = openFixture("docs/asked.md");
      const walked = present("docs/walked.md", "presentation");
      const state = projectFilesState(PROJECT);
      expect(focusedFilesBufferKey(PROJECT)).toBe(asked);
      // The presented file is open beside it, not in front of it.
      expect(state.order).toEqual([asked, walked]);

      applyFilesBufferLoad(PROJECT, asked, loadedResponse("asked"));
      expect(projectFilesState(PROJECT).activeKey).toBe(asked);
    });

    it("lets a presentation aim once the reader's file is on screen", () => {
      const asked = openFixture("docs/asked.md");
      applyFilesBufferLoad(PROJECT, asked, loadedResponse("asked"));
      const walked = present("docs/walked.md", "presentation");
      expect(focusedFilesBufferKey(PROJECT)).toBe(walked);
    });

    it("lets a reader displace a presentation that is still loading", () => {
      present("docs/walked.md", "presentation");
      const asked = openFixture("docs/asked.md");
      expect(focusedFilesBufferKey(PROJECT)).toBe(asked);
    });

    it("keeps a reader's aim through a presentation-origin activation", () => {
      const asked = openFixture("docs/asked.md");
      const other = present("docs/other.md", "presentation");
      setFilesActiveBuffer(PROJECT, other, "presentation");
      expect(focusedFilesBufferKey(PROJECT)).toBe(asked);
      setFilesActiveBuffer(PROJECT, other);
      expect(focusedFilesBufferKey(PROJECT)).toBe(other);
    });

    it("re-aiming the reader's own file is not a displacement", () => {
      const asked = openFixture("docs/asked.md");
      setFilesActiveBuffer(PROJECT, asked, "presentation");
      expect(focusedFilesBufferKey(PROJECT)).toBe(asked);
    });
  });

  it("opens buffers once and re-activates on reopen", () => {
    const key = openFixture("src/a.ts");
    openFixture("src/b.ts");
    expect(projectFilesState(PROJECT).order).toHaveLength(2);
    expect(projectFilesState(PROJECT).activeKey).not.toBe(key);

    const again = openFixture("src/a.ts");
    expect(again).toBe(key);
    expect(projectFilesState(PROJECT).order).toHaveLength(2);
    expect(projectFilesState(PROJECT).activeKey).toBe(key);
  });

  it.each([false, true])("reuses an already loaded identity without overwriting it (dirty=%s)", (dirty) => {
    const stable = openFilesBuffer(PROJECT, {
      intent: "permanent", rootId: "r1", rootLabel: "repo", path: "Cargo.toml", fileId: "file-a",
    });
    applyFilesBufferLoad(PROJECT, stable, { ...loadedResponse("current text"), path: "Cargo.toml", file_id: "file-a" });
    if (dirty) applyFilesBufferDraft(PROJECT, stable, "unsaved edits");
    const duplicate = openFixture("alias/Cargo.toml");
    const settled = applyFilesBufferLoad(PROJECT, duplicate, {
      ...loadedResponse("stale disk content"), path: "Cargo.toml", file_id: "file-a",
    });
    expect(settled).toBe(stable);
    const state = projectFilesState(PROJECT);
    expect(state.order).toEqual([stable]);
    expect(state.activeKey).toBe(stable);
    expect(state.pendingKey).toBeNull();
    expect(state.byKey[stable]).toMatchObject({
      content: { state: "source", base: "current text", text: dirty ? "unsaved edits" : "current text" }, dirty,
    });
  });

  it("reuses a stable-id tab when a path-addressed open names the same file", () => {
    const stable = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "src/a.ts",
      fileId: "file-a",
    });
    applyFilesBufferLoad(PROJECT, stable, {
      ...loadedResponse("hello"),
      file_id: "file-a",
      path: "src/a.ts",
    });

    const reopened = openFixture("src/a.ts");
    expect(reopened).toBe(stable);
    expect(projectFilesState(PROJECT).order).toEqual([stable]);
    expect(projectFilesState(PROJECT).activeKey).toBe(stable);

    const replacement = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "src/a.ts",
      fileId: "file-b",
    });
    expect(replacement).not.toBe(stable);
    expect(projectFilesState(PROJECT).order).toEqual([stable, replacement]);
    expect(openFixture("src/a.ts")).toBe(replacement);
    expect(projectFilesState(PROJECT).order).toEqual([stable, replacement]);
  });

  // Tabs belong to the project, so a root-set change leaves them open.
  it("keeps open tabs across a change to the project's roots", () => {
    openFixture("a.ts");
    openFixture("b.ts");
    expect(projectFilesState(PROJECT).order).toEqual([
      fileBufferKey("r1", "a.ts"),
      fileBufferKey("r1", "b.ts"),
    ]);
  });

  it("re-runs computations tracking buffer state as tabs open", () => {
    createRoot((dispose) => {
      const len = createMemo(() => projectFilesState(PROJECT).order.length);
      expect(len()).toBe(0);
      openFixture("a.ts");
      expect(len()).toBe(1);
      dispose();
    });
  });

  it("keeps each explicit file open in its own durable tab", () => {
    const first = openFixture("src/first.ts");
    const second = openFixture("src/second.ts");

    const state = projectFilesState(PROJECT);
    expect(state.order).toEqual([first, second]);
    expect(state.byKey[first]!.preview).toBe(false);
    expect(state.byKey[second]!.preview).toBe(false);
    expect(state.activeKey).toBe(second);
  });

  it("clears loading as soon as the source read lands", () => {
    const key = openFixture("a.ts");
    expect(projectFilesState(PROJECT).byKey[key]!.loading).toBe(true);
    applyFilesBufferLoad(PROJECT, key, loadedResponse("hello"));
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.loading).toBe(false);
    expect(filesBufferText(required(buf))).toBe("hello");
  });

  it("keeps a document's dirty indication while its first paint waits for replica adoption", () => {
    const key = openFixture("dirty.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse(""), { text: "host draft", eol: "lf", mixed: false, dirty: true });
    expect(projectFilesState(PROJECT).byKey[key]?.dirty).toBe(true);
    applyFilesBufferLoad(PROJECT, key, loadedResponse(""), { text: "confirmed base", eol: "lf", mixed: false, dirty: false });
    expect(projectFilesState(PROJECT).byKey[key]?.dirty).toBe(true);
  });

  it("stores encoding and writable from the read", () => {
    const key = openFixture("bom.txt");
    applyFilesBufferLoad(PROJECT, key, {
      ...loadedResponse("hello"),
      encoding: "utf-8-bom",
      writable: false,
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.encoding).toBe("utf-8-bom");
    expect(buf.writable).toBe(false);
  });

  it("keeps the detected EOL and makes an explicit style change savable", () => {
    const key = openFixture("windows.txt");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("one\r\ntwo\n"));
    let buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.eol).toBe("lf");
    expect(buf.mixedEol).toBe(true);
    setFilesBufferEol(PROJECT, key, "crlf");
    buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.eol).toBe("crlf");
    expect(buf.mixedEol).toBe(false);
    expect(buf.dirty).toBe(true);
  });

  it("ends a buffer's preview lease the moment an EOL change dirties it", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "windows.txt",
    });
    applyFilesBufferLoad(PROJECT, key, loadedResponse("one\ntwo\n"));
    expect(projectFilesState(PROJECT).byKey[key]!.preview).toBe(true);

    setFilesBufferEol(PROJECT, key, "crlf");
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.dirty).toBe(true);
    expect(buf.preview).toBe(false);
  });

  it("ends a buffer's preview lease when an editor document sync dirties it via EOL alone", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    applyFilesBufferLoad(PROJECT, key, loadedResponse("same"));
    expect(projectFilesState(PROJECT).byKey[key]!.preview).toBe(true);

    applyFilesBufferEditorDocument(PROJECT, key, {
      encoding: "utf-8",
      fileId: "file-1",
      documentId: "document-1",
      revision: 1,
      diverged: false,
      absent: false,
      heldAgentVersionId: null,
      localGeneration: 1, dirty: true,
      baseSha256: "abc123",
      sizeBytes: 4,
      eol: "crlf",
      baseEol: "lf",
      mixedEol: false,
      baseMixedEol: false,
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.dirty).toBe(true);
    expect(buf.preview).toBe(false);
  });

  it("maps unsupported encoding to an info buffer", () => {
    const key = openFixture("wide.txt");
    applyFilesBufferUnsupportedEncoding(PROJECT, key, "unknown");
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.kind).toBe("info");
    expect(buf.unsupportedEncodingDetected).toBe("unknown");
    expect(buf.loadError).toBeNull();
  });

  it("tracks dirty against loaded base content", () => {
    const key = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("hello"));
    const buf = () => projectFilesState(PROJECT).byKey[key]!;
    expect(buf().loading).toBe(false);
    expect(buf().dirty).toBe(false);

    applyFilesBufferDraft(PROJECT, key, "hello world");
    expect(buf().dirty).toBe(true);

    applyFilesBufferDraft(PROJECT, key, "hello");
    expect(buf().dirty).toBe(false);
  });

  it("marks typing dirty once and leaves later keystrokes off the store", () => {
    const key = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("hello"));
    const buf = () => projectFilesState(PROJECT).byKey[key]!;
    markFilesBufferTyping(PROJECT, key);
    const revision = buf().editRevision;
    expect(buf().dirty).toBe(true);
    markFilesBufferTyping(PROJECT, key);
    markFilesBufferTyping(PROJECT, key);
    expect(buf().editRevision).toBe(revision);
  });

  it("save rebases the buffer; discard drops the draft", () => {
    const key = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("base"));
    applyFilesBufferDraft(PROJECT, key, "edited");
    const buf = () => projectFilesState(PROJECT).byKey[key]!;

    applyFilesBufferSaved(PROJECT, key, {
      content: "edited",
      sha256: "def456",
      sizeBytes: 6,
      eol: buf().eol,
      editRevision: buf().editRevision,
      fileId: "file-1",
      documentId: "document-1",
      documentRevision: 2,

    });
    expect(buf().dirty).toBe(false);
    expect(filesBufferBase(required(buf()))).toBe("edited");
    expect(buf().baseSha256).toBe("def456");

    applyFilesBufferDraft(PROJECT, key, "edited again");
    discardFilesBufferDraft(PROJECT, key);
    expect(filesBufferText(required(buf()))).toBe("edited");
    expect(buf().dirty).toBe(false);
  });

  it("rebases a completed save without clobbering newer typing", () => {
    const key = openFixture("src/slow-save.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("base"));
    applyFilesBufferDraft(PROJECT, key, "sent");
    const sent = projectFilesState(PROJECT).byKey[key]!;
    const editRevision = sent.editRevision;
    applyFilesBufferDraft(PROJECT, key, "typed while saving");

    applyFilesBufferSaved(PROJECT, key, {
      content: "sent",
      sha256: "saved-sha",
      sizeBytes: 4,
      eol: sent.eol,
      editRevision,
      fileId: "file-1",
      documentId: "document-1",
      documentRevision: 2,

    });

    const current = projectFilesState(PROJECT).byKey[key]!;
    expect(filesBufferBase(required(current))).toBe("sent");
    expect(filesBufferText(required(current))).toBe("typed while saving");
    expect(current.dirty).toBe(true);
  });

  it("discard restores the loaded line-ending shape", () => {
    const key = openFixture("mixed.txt");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("one\r\ntwo\n"));
    setFilesBufferEol(PROJECT, key, "crlf");
    discardFilesBufferDraft(PROJECT, key);

    const current = projectFilesState(PROJECT).byKey[key]!;
    expect(current.eol).toBe("lf");
    expect(current.mixedEol).toBe(true);
    expect(current.dirty).toBe(false);
  });

  it("close moves the selection and reload re-marks loading", () => {
    const a = openFixture("a.ts");
    const b = openFixture("b.ts");
    setFilesActiveBuffer(PROJECT, a);

    void closeFilesBuffer(PROJECT, a);
    expect(projectFilesState(PROJECT).order).toEqual([b]);
    expect(projectFilesState(PROJECT).activeKey).toBe(b);
    expect(projectFilesState(PROJECT).byKey[a]).toBeUndefined();

    applyFilesBufferLoad(PROJECT, b, loadedResponse("x"));
    markFilesBufferLoading(PROJECT, b);
    expect(projectFilesState(PROJECT).byKey[b]!.loading).toBe(true);
  });

  it("materializes pending input and refuses to close without a durable document", async () => {
    const key = openFixture("a.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("base"));
    let materializedWhileOpen = false;
    scheduleFilesDraftSync(PROJECT, key, () => {
      materializedWhileOpen = projectFilesState(PROJECT).byKey[key] != null;
      applyFilesBufferDraft(PROJECT, key, "latest");
    });

    expect(closeFilesBuffer(PROJECT, key)).toBeNull();

    expect(materializedWhileOpen).toBe(true);
    expect(filesBufferText(projectFilesState(PROJECT).byKey[key]!)).toBe("latest");
  });

  it("treats root scope as every descendant and drops only that root", () => {
    const first = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, first, loadedResponse("base"));
    applyFilesBufferDraft(PROJECT, first, "changed");
    const second = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r2",
      rootLabel: "docs",
      path: "guide.md",
    });
    applyFilesBufferLoad(PROJECT, second, loadedResponse("guide"));

    expect(dirtyFilesBuffersUnderPath(PROJECT, "r1", ".").map((b) => b.key)).toEqual([first]);
    dropFilesBuffersForRoot(PROJECT, "r1");
    expect(projectFilesState(PROJECT).byKey[first]).toBeUndefined();
    expect(projectFilesState(PROJECT).order).toEqual([second]);
  });

  it("retires a deleted project's buffers without consulting its root registry", () => {
    const first = openFixture("src/a.ts");
    openFilesBuffer(PROJECT, { intent: "permanent", rootId: "r2", rootLabel: "docs", path: "guide.md" });
    const other = openFilesBuffer("p2", { intent: "permanent", rootId: "r3", rootLabel: "other", path: "keep.md" });
    const sync = vi.fn();
    scheduleFilesDraftSync(PROJECT, first, sync);

    dropFilesBuffersForProject(PROJECT);
    dropFilesBuffersForProject(PROJECT);
    flushFilesDraftSyncFor(PROJECT, first);

    expect(projectFilesState(PROJECT).order).toEqual([]);
    expect(projectFilesState(PROJECT).byKey).toEqual({});
    expect(projectFilesState(PROJECT).activeKey).toBeNull();
    expect(projectFilesState(PROJECT).pendingKey).toBeNull();
    expect(projectFilesState("p2").order).toEqual([other]);
    expect(sync).not.toHaveBeenCalled();
  });

  it("reorders tabs, clamping the target index and ignoring unknown keys", () => {
    const a = openFixture("src/a.ts");
    const b = openFixture("src/b.ts");
    const c = openFixture("src/c.ts");

    moveFilesBuffer(PROJECT, a, 2);
    expect(projectFilesState(PROJECT).order).toEqual([b, c, a]);

    moveFilesBuffer(PROJECT, a, -5);
    expect(projectFilesState(PROJECT).order).toEqual([a, b, c]);

    moveFilesBuffer(PROJECT, b, 99);
    expect(projectFilesState(PROJECT).order).toEqual([a, c, b]);

    moveFilesBuffer(PROJECT, "missing", 0);
    expect(projectFilesState(PROJECT).order).toEqual([a, c, b]);
    expect(projectFilesState(PROJECT).activeKey).toBe(c);
  });

  it("keeps buffers per project", () => {
    openFixture("a.ts");
    expect(projectFilesState("other").order).toHaveLength(0);
  });

  it("retargets buffers under a renamed folder", () => {
    const key = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, key, loadedResponse("x"));
    applyFilesBufferDraft(PROJECT, key, "dirty");
    retargetFilesBufferStoreUnderPath(PROJECT, "r1", "src", "lib");
    const next = projectFilesState(PROJECT).byKey[fileBufferKey("r1", "lib/a.ts")];
    expect(next?.path).toBe("lib/a.ts");
    expect(next?.dirty).toBe(true);
  });

  it.each([
    ["docs/draft 🐺.md", "docs/draft 🐺.md", "docs/renamed café.md", "docs/renamed café.md"],
    ["docs/draft 🐺.md", "docs", "moved café", "moved café/draft 🐺.md"],
  ])("keeps a retained file identity reactive after moving %s", (path, from, to, expected) => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent", rootId: "r1", rootLabel: "repo", path, fileId: "stable-file",
    });
    applyFilesBufferLoad(PROJECT, key, { ...loadedResponse("base"), file_id: "stable-file" });
    applyFilesBufferDraft(PROJECT, key, "unsaved 🐺");
    const retained = projectFilesState(PROJECT).byKey[key]!;
    createRoot((dispose) => {
      const address = createMemo(() => `${retained.rootId}/${retained.path}`);
      expect(address()).toBe(`r1/${path}`);
      retargetFilesBufferStoreUnderPath(PROJECT, "r1", from, to);
      expect(projectFilesState(PROJECT).byKey[key]).toBe(retained);
      expect(address()).toBe(`r1/${expected}`);
      expect(retained.name).toBe(expected.split("/").pop());
      expect(filesBufferText(required(retained))).toBe("unsaved 🐺");
      expect(retained.dirty).toBe(true);
      expect(projectFilesState(PROJECT).activeKey).toBe(key);
      dispose();
    });
  });

  it("ignores draft updates for non-text buffers", () => {
    const key = openFixture("photo.png");
    applyFilesBufferLoad(PROJECT, key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "photo.png",
      content: "",
      over_limit: false,
      writable: true,
      binary: true,
      mime: "image/png",
      size_bytes: 100,
    });
    applyFilesBufferDraft(PROJECT, key, "nope");
    expect(projectFilesState(PROJECT).byKey[key]!.dirty).toBe(false);
    expect(projectFilesState(PROJECT).byKey[key]!.kind).toBe("image");
  });

  it("clears condenseContext when a non-text buffer loads", () => {
    const key = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: "data.bin",
      intent: "transient",
      condenseContext: true,
    });
    expect(projectFilesState(PROJECT).byKey[key]!.condenseContext).toBe(true);

    applyFilesBufferLoad(PROJECT, key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "data.bin",
      content: "",
      over_limit: false,
      writable: true,
      binary: true,
      mime: "application/octet-stream",
      size_bytes: 512,
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.kind).toBe("info");
    expect(buf.condenseContext).toBe(false);

    openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: "data.bin",
      intent: "transient",
      condenseContext: true,
    });
    expect(projectFilesState(PROJECT).byKey[key]!.condenseContext).toBe(false);
  });

  it("clears condenseContext when unsupported encoding is detected", () => {
    const key = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: "weird.txt",
      intent: "transient",
      condenseContext: true,
    });
    expect(projectFilesState(PROJECT).byKey[key]!.condenseContext).toBe(true);

    applyFilesBufferUnsupportedEncoding(PROJECT, key, "shift_jis");
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.kind).toBe("info");
    expect(buf.condenseContext).toBe(false);
  });

  it("assigns info kind for over-limit metadata loads", () => {
    const key = openFixture("big.log");
    applyFilesBufferLoad(PROJECT, key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "big.log",
      content: "",
      over_limit: true,
      writable: true,
      binary: false,
      mime: "text/plain",
      modified_at: "2026-01-01T00:00:00Z",
      size_bytes: 5 * 1024 * 1024,
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    expect(buf.kind).toBe("info");
    expect(buf.overLimit).toBe(true);
    expect(bufferKindFromSourceRead({
      content: "",
      over_limit: true,
      binary: false,
      mime: "text/plain",
    })).toBe("info");
  });

  it("keeps at most one transient preview and replaces it in place", () => {
    const a = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    expect(projectFilesState(PROJECT).byKey[a]!.preview).toBe(true);
    const b = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "b.ts",
    });
    expect(projectFilesState(PROJECT).order).toEqual([b]);
    expect(projectFilesState(PROJECT).byKey[a]).toBeUndefined();
    expect(projectFilesState(PROJECT).byKey[b]!.preview).toBe(true);

    openFixture("c.ts");
    const d = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "d.ts",
    });
    const previews = projectFilesState(PROJECT).order.filter(
      (k) => projectFilesState(PROJECT).byKey[k]!.preview,
    );
    expect(previews).toEqual([d]);
  });

  it("never silently discards a dirtied preview tab when a single-click opens another file", () => {
    const a = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    applyFilesBufferLoad(PROJECT, a, loadedResponse("one\ntwo\n"));
    expect(projectFilesState(PROJECT).byKey[a]!.preview).toBe(true);

    setFilesBufferEol(PROJECT, a, "crlf");
    expect(projectFilesState(PROJECT).byKey[a]!.dirty).toBe(true);

    const b = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "b.ts",
    });

    const state = projectFilesState(PROJECT);
    expect(state.order).toEqual([a, b]);
    expect(state.byKey[a]).toBeDefined();
    expect(state.byKey[a]!.dirty).toBe(true);
    expect(state.byKey[a]!.eol).toBe("crlf");
    expect(state.byKey[a]!.preview).toBe(false);
    expect(state.byKey[b]!.preview).toBe(true);
  });

  it("promotes preview by flag flip without remounting identity", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    applyFilesBufferLoad(PROJECT, key, loadedResponse("base"));
    expect(projectFilesState(PROJECT).byKey[key]!.preview).toBe(true);

    promoteFilesBuffer(PROJECT, key);
    expect(projectFilesState(PROJECT).byKey[key]!.preview).toBe(false);
    expect(projectFilesState(PROJECT).byKey[key]!.key).toBe(key);

    void closeFilesBuffer(PROJECT, key);
    const again = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    applyFilesBufferLoad(PROJECT, again, loadedResponse("base"));
    applyFilesBufferDraft(PROJECT, again, "edited");
    expect(projectFilesState(PROJECT).byKey[again]!.preview).toBe(false);
    expect(projectFilesState(PROJECT).byKey[again]!.dirty).toBe(true);

    const preview = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "p.ts",
    });
    applyFilesBufferLoad(PROJECT, preview, loadedResponse("x"));
    expect(projectFilesState(PROJECT).byKey[preview]!.preview).toBe(true);
    expect(projectFilesState(PROJECT).byKey[preview]!.dirty).toBe(false);

    openFixture("z.ts");
    const t = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "drag.ts",
    });
    moveFilesBuffer(PROJECT, t, projectFilesState(PROJECT).order.length - 1);
    expect(projectFilesState(PROJECT).byKey[t]!.preview).toBe(false);

    const opened = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "omni.ts",
    });
    openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "omni.ts",
    });
    expect(projectFilesState(PROJECT).byKey[opened]!.preview).toBe(false);
  });

  it("pins sort first, skip transient reuse, spare bulk closes", () => {
    const a = openFixture("a.ts");
    const b = openFixture("b.ts");
    const c = openFixture("c.ts");
    setFilesBufferPinned(PROJECT, b, true);
    expect(projectFilesState(PROJECT).order[0]).toBe(b);
    expect(projectFilesState(PROJECT).byKey[b]!.pinned).toBe(true);

    openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "preview.ts",
    });
    expect(projectFilesState(PROJECT).order).toContain(b);
    expect(projectFilesState(PROJECT).byKey[b]!.preview).toBe(false);

    expect(filesCloseTargetKeys(PROJECT, "closeOthers", a)).not.toContain(b);
    expect(filesCloseTargetKeys(PROJECT, "closeSaved", a)).not.toContain(b);
    expect(filesCloseTargetKeys(PROJECT, "closeToTheRight", a)).not.toContain(b);
    expect(filesCloseTargetKeys(PROJECT, "closeAll", a)).toContain(b);

    // Drag of unpinned tab clamps outside the pinned group.
    moveFilesBuffer(PROJECT, c, 0);
    expect(projectFilesState(PROJECT).order[0]).toBe(b);
    expect(projectFilesState(PROJECT).order).toContain(c);

    toggleFilesBufferPinned(PROJECT, b);
    expect(projectFilesState(PROJECT).byKey[b]!.pinned).toBe(false);

    const preview = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "pin-me.ts",
    });
    setFilesBufferPinned(PROJECT, preview, true);
    expect(projectFilesState(PROJECT).byKey[preview]!.preview).toBe(false);

    void closeFilesBuffers(PROJECT, [a]);
    expect(projectFilesState(PROJECT).byKey[a]).toBeUndefined();
  });

});

describe("project-files-buffers presentation retention", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
  });

  it("keeps the settled file painted while the next one loads", () => {
    const first = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, first, loadedResponse("first"));
    expect(projectFilesState(PROJECT).activeKey).toBe(first);

    const second = openFixture("src/b.ts");
    expect(projectFilesState(PROJECT).activeKey).toBe(first);
    expect(projectFilesState(PROJECT).pendingKey).toBe(second);
    expect(focusedFilesBufferKey(PROJECT)).toBe(second);

    applyFilesBufferLoad(PROJECT, second, loadedResponse("second"));
    expect(projectFilesState(PROJECT).activeKey).toBe(second);
    expect(projectFilesState(PROJECT).pendingKey).toBe(null);
  });

  it("presents a failed open so its error is the thing on screen", () => {
    const first = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, first, loadedResponse("first"));
    const second = openFixture("src/missing.ts");
    expect(projectFilesState(PROJECT).activeKey).toBe(first);

    applyFilesBufferLoadError(PROJECT, second, "gone");
    expect(projectFilesState(PROJECT).activeKey).toBe(second);
    expect(projectFilesState(PROJECT).pendingKey).toBe(null);
  });

  it("takes the surface when nothing settled is on it", () => {
    const first = openFixture("src/a.ts");
    const second = openFixture("src/b.ts");
    expect(projectFilesState(PROJECT).activeKey).toBe(second);
    expect(projectFilesState(PROJECT).pendingKey).toBe(null);
    expect(first).not.toBe(second);
  });

  it("keeps settled content through a long read", async () => {
    vi.useFakeTimers();
    try {
      const first = openFixture("src/a.ts");
      applyFilesBufferLoad(PROJECT, first, loadedResponse("first"));
      const slow = openFixture("src/slow.ts");
      expect(projectFilesState(PROJECT).activeKey).toBe(first);

      await vi.advanceTimersByTimeAsync(5_000);
      expect(projectFilesState(PROJECT).activeKey).toBe(first);
      expect(projectFilesState(PROJECT).pendingKey).toBe(slow);
      expect(projectFilesState(PROJECT).byKey[slow]?.loading).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("presents a version-backed buffer without waiting for a source read", () => {
    const first = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, first, loadedResponse("first"));
    const stepped = openFixture("src/b.ts");
    expect(projectFilesState(PROJECT).pendingKey).toBe(stepped);

    // Historical bytes are available while the working-file read is pending.
    presentFilesBufferVersion(PROJECT, stepped);
    expect(projectFilesState(PROJECT).activeKey).toBe(stepped);
    expect(projectFilesState(PROJECT).pendingKey).toBe(null);
    expect(projectFilesState(PROJECT).byKey[stepped]?.loading).toBe(true);

    presentFilesBufferVersion(PROJECT, first);
    expect(projectFilesState(PROJECT).activeKey).toBe(stepped);
  });

  it("holds the settled file when a loading tab is selected explicitly", () => {
    const first = openFixture("src/a.ts");
    applyFilesBufferLoad(PROJECT, first, loadedResponse("first"));
    const second = openFixture("src/b.ts");
    expect(projectFilesState(PROJECT).pendingKey).toBe(second);

    setFilesActiveBuffer(PROJECT, first);
    expect(projectFilesState(PROJECT).activeKey).toBe(first);
    expect(projectFilesState(PROJECT).pendingKey).toBe(null);

    setFilesActiveBuffer(PROJECT, second);
    expect(projectFilesState(PROJECT).activeKey).toBe(first);
    expect(projectFilesState(PROJECT).pendingKey).toBe(second);
  });
});
