import {
  setupFilesEditorTests,
} from "./files-editor-test-harness.ts";

import "../../test/document-outbox-fixture.ts";

import {
  PROJECT,
  PROJECT_SOURCE_IDENTITY,
  ROOTS,
  loadedBuffer,
  editorProps,
  resetProjectFilesViewTest,
} from "../components/project-files-view-test-harness.ts";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";

import { createSignal } from "solid-js";

import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { FilesEditor } from "./FilesEditor.tsx";
import { FilesInfoCard } from "../components/FilesInfoCard.tsx";
import {
  applyFilesBufferDraft,
  applyFilesBufferLoad,
  applyFilesBufferUnsupportedEncoding,
  openFilesBuffer,
} from "../documents/project-files-buffers.ts";
import {
  projectFilesState,
  type FileBuffer,
} from "../documents/files-buffer-state.ts";

describe("FilesEditor", () => {
  afterEach(() => registerNoticePublisher(null));

  setupFilesEditorTests();

  it("does not mount the text editor for over-limit info buffers", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "big.bin",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "big.bin",
      content: "",
      over_limit: true,
      writable: true,
      binary: false,
      mime: "text/plain",
      modified_at: "2026-01-01T00:00:00Z",
      size_bytes: 5_000_000,
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    render(() => (
      <FilesInfoCard
        buffer={buf}
        roots={ROOTS}
        onInnerLayerChange={vi.fn()}
        onRevealSegment={vi.fn()}
      />
    ));
    expect(screen.getByTestId("files-info-card")).toBeTruthy();
    expect(screen.getByTestId("files-info-explain").textContent).toBe("This file is larger than the in-app editor opens.");
    expect(screen.queryByTestId("files-editor-host")).toBeNull();
    expect(
      screen.getByTestId("files-info-card").querySelector(".den-files-toolbar"),
    ).toBeTruthy();
  });

  it("shows the unsupported-encoding info card with locked copy", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "wide.txt",
    });
    applyFilesBufferUnsupportedEncoding(PROJECT, key, "unknown");
    const buf = projectFilesState(PROJECT).byKey[key]!;
    render(() => (
      <FilesInfoCard
        buffer={buf}
        roots={ROOTS}
        onInnerLayerChange={vi.fn()}
        onRevealSegment={vi.fn()}
      />
    ));
    expect(screen.getByTestId("files-info-title").textContent).toBe(
      "Can't open this file safely",
    );
    expect(screen.getByTestId("files-info-explain").textContent).toContain(
      "unsupported or malformed text encoding",
    );
    expect(screen.getByTestId("open-in-button")).toBeTruthy();
    expect(screen.queryByTestId("files-info-copy-path-btn")).toBeNull();
  });

  it("offers an explicit BOM-less UTF-16 reopen choice", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "utf16.txt",
    });
    applyFilesBufferUnsupportedEncoding(PROJECT, key, "unknown");
    const buf = projectFilesState(PROJECT).byKey[key]!;
    const onReopenAsUTF16 = vi.fn();
    render(() => (
      <FilesInfoCard
        buffer={buf}
        roots={ROOTS}
        onInnerLayerChange={vi.fn()}
        onRevealSegment={vi.fn()}
        onReopenAsUTF16={onReopenAsUTF16}
      />
    ));
    fireEvent.click(screen.getByTestId("files-info-open-utf16le-btn"));
    fireEvent.click(screen.getByTestId("files-info-open-utf16be-btn"));
    expect(onReopenAsUTF16).toHaveBeenNthCalledWith(1, "utf-16le");
    expect(onReopenAsUTF16).toHaveBeenNthCalledWith(2, "utf-16be");
  });

  it("offers the explicit UTF-16 reopen choice for a binary result", async () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "bomless.txt",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "bomless.txt",
      content: "",
      over_limit: false,
      writable: true,
      binary: true,
      size_bytes: 12,
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    render(() => (
      <FilesInfoCard
        buffer={buf}
        roots={ROOTS}
        onInnerLayerChange={vi.fn()}
        onRevealSegment={vi.fn()}
        onReopenAsUTF16={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByTestId("files-info-open-text-btn"));
    expect(await screen.findByTestId("files-info-open-utf16le-menu")).toBeTruthy();
    expect(screen.getByTestId("files-info-open-utf16be-menu")).toBeTruthy();
  });

  it("announces file-approvals read-only before typing", () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "ro.txt",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "ro.txt",
      content: "locked\n",
      over_limit: false,
      writable: false,
      binary: false,
      size_bytes: 7,
      sha256: "ro-sha",
      encoding: "utf-8",
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    const onMakeEditable = vi.fn();
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        onMakeEditable={onMakeEditable}
      />
    ));
    expect(screen.getByTestId("files-editor-mode").textContent).toBe(
      "Read-only file",
    );
    expect(screen.getByTestId("files-editor-mode").getAttribute("data-editor-identity"))
      .toBe("read-only-file");
    const action = screen.getByRole("button", { name: "Make editable" });
    expect(action.textContent).toBe("");
    fireEvent.click(action);
    expect(onMakeEditable).toHaveBeenCalledOnce();
  });

  it("names a worker draft and offers only the open-current icon action", () => {
    const buf = loadedBuffer({ jobId: "job-1" });
    const onOpenCurrent = vi.fn();
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        editable={false}
        onOpenCurrent={onOpenCurrent}
      />
    ));
    expect(screen.getByTestId("files-editor-mode").textContent).toBe("Worker draft");
    const actions = document.querySelectorAll("[data-state-action]");
    expect(actions).toHaveLength(1);
    const action = screen.getByRole("button", { name: "Open current file" });
    expect(action.textContent).toBe("");
    fireEvent.click(action);
    expect(onOpenCurrent).toHaveBeenCalledOnce();
  });

  it("shows shared editing without an exclusive takeover action", () => {
    const buf = { ...loadedBuffer(), editorParticipants: 2 };
    render(() => <FilesEditor {...editorProps(buf)} editable />);
    expect(document.querySelector(".den-files-editor__status")?.getAttribute("data-editor-identity")).toBe("editing");
    expect(screen.queryByTestId("files-editor-mode")).toBeNull();
    expect(document.querySelectorAll("[data-state-action]")).toHaveLength(0);
    expect(screen.queryByRole("button", { name: "Edit here" })).toBeNull();
  });

  it("offers a document retry after an initialization error without reloading or discarding text", () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    const notices = () => selectProjectNoticeGroups(store.index()).find(group => group.projectId === PROJECT)?.notices ?? [];
    const buffer = loadedBuffer();
    const retry = vi.fn();
    const [opening, setOpening] = createSignal<NonNullable<FileBuffer["editorOpening"]>>({ status: "opening" });
    render(() => (
      <FilesEditor {...editorProps(buffer)}
        buffer={{ ...buffer, editorOpening: opening() }} editable={false} onRetryEditing={retry} />
    ));
    expect(screen.getByTestId("files-editor-mode").textContent).toBe("Opening file…");
    expect(document.querySelectorAll("[data-state-action]")).toHaveLength(0);
    expect(notices()).toEqual([]);
    setOpening({ status: "error", message: "Unable to read the local checkpoint." });
    expect(screen.getByTestId("files-editor-mode").textContent).toBe("Editing unavailable");
    expect(notices()).toMatchObject([{ code: "files_editing_unavailable", message: "Unable to read the local checkpoint." }]);
    fireEvent.click(screen.getByRole("button", { name: "Retry editing" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("presents automatic reconnect as status while retaining the file text", () => {
    const buf = { ...loadedBuffer(), editorOpening: { status: "reconnecting" as const, message: "Waiting for the connection." } };
    render(() => <FilesEditor {...editorProps(buf)} editable={false} />);
    expect(screen.getByTestId("files-editor-mode").textContent).toBe("Reconnecting…");
    expect(screen.getByText("Waiting for the connection.")).toBeTruthy();
    expect(document.querySelectorAll("[data-state-action]")).toHaveLength(0);
  });

  for (const [encoding, label] of [
    ["utf-8-bom", "UTF-8 BOM"],
    ["utf-16le", "UTF-16 LE"],
    ["utf-16le-bom", "UTF-16 LE BOM"],
    ["utf-16be", "UTF-16 BE"],
    ["utf-16be-bom", "UTF-16 BE BOM"],
  ] as const) {
    it(`shows the ${label} encoding chip`, () => {
      const key = openFilesBuffer(PROJECT, {
        intent: "permanent",
        rootId: "r1",
        rootLabel: "repo",
        path: `${encoding}.txt`,
      });
      applyFilesBufferLoad(PROJECT, key, {
        ...PROJECT_SOURCE_IDENTITY,
        file_id: "",
        version_id: "",
        path: `${encoding}.txt`,
        content: "hello\n",
        over_limit: false,
        writable: true,
        binary: false,
        size_bytes: 12,
        sha256: `${encoding}-sha`,
        encoding,
      });
      const buf = projectFilesState(PROJECT).byKey[key]!;
      render(() => <FilesEditor {...editorProps(buf)} />);
      expect(screen.getByTestId("files-editor-encoding-chip").textContent).toBe(
        label,
      );
    });
  }

  it("shows save chrome only when dirty and wires Save", async () => {
    const buf = loadedBuffer();
    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);
    expect(screen.queryByTestId("files-editor-save")).toBeNull();

    applyFilesBufferDraft(PROJECT, buf.key, "changed");
    await waitFor(() => {
      expect(screen.getByTestId("files-editor-save")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("files-editor-save"));
    expect(props.onSave).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByTestId("files-editor-discard"));
    expect(props.onDiscard).toHaveBeenCalledOnce();
  });

  it("disables Save until disk divergence is resolved", async () => {
    const buf = loadedBuffer();
    const props = editorProps(buf);
    render(() => <FilesEditor {...props} conflict />);
    applyFilesBufferDraft(PROJECT, buf.key, "unsaved work");
    await waitFor(() => expect((screen.getByTestId("files-editor-save") as HTMLButtonElement).disabled).toBe(true));
    expect((screen.getByTestId("files-editor-conflict-merge") as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByTestId("files-editor-conflict-reload") as HTMLButtonElement).disabled).toBe(false);
  });

  it("divergence banner exposes the two actions that resolve it", async () => {
    const buf = loadedBuffer();
    const props = {
      ...editorProps(buf),
      conflict: true,
    };
    render(() => <FilesEditor {...props} />);
    expect(screen.getByTestId("files-editor-conflict").textContent).toContain(
      "This file changed on disk while you have unsaved edits.",
    );
    fireEvent.click(screen.getByTestId("files-editor-conflict-merge"));
    fireEvent.click(screen.getByTestId("files-editor-conflict-reload"));
    expect(props.onMerge).toHaveBeenCalledOnce();
    expect(props.onReload).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("files-editor-conflict-dismiss")).toBeNull();
  });

  it("says the AI's edit survives a divergence that cannot be saved", () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} conflict heldAgentEdit />);

    expect(screen.getByTestId("files-editor-conflict").textContent).toContain(
      "The AI's edit is kept in version history.",
    );
  });

  it("says nothing about the AI when the draft holds only the person's edits", () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} conflict />);

    expect(screen.getByTestId("files-editor-conflict")).toBeTruthy();
    expect(screen.queryByTestId("files-editor-conflict-held")).toBeNull();
  });

  it("save-409 conflict reuses the same divergence banner surface", () => {
    const buf = loadedBuffer();
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        conflict
        saveError="conflict"
      />
    ));
    expect(screen.getByTestId("files-editor-conflict")).toBeTruthy();
    expect(screen.getByTestId("files-editor-conflict-merge")).toBeTruthy();
  });
});

describe("bars above the editor", () => {
  beforeEach(() => {
    resetProjectFilesViewTest();
  });

  it("folds save errors into the conflict banner", () => {
    const buf = loadedBuffer();
    applyFilesBufferLoad(PROJECT, buf.key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      content: "hello\n",
      sha256: "sha-bars",
      encoding: "utf-8",
      over_limit: false,
      binary: false,
      size_bytes: 6,
      path: buf.path,
      writable: true,
    });
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        conflict
        saveError="save failed"
      />
    ));
    expect(screen.getByTestId("files-editor-conflict")).toBeTruthy();
  });
});