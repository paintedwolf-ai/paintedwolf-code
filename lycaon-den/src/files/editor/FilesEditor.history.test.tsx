import { setupFilesEditorTests } from "./files-editor-test-harness.ts";

import "../../test/document-outbox-fixture.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import {
  fileVersionFromSnapshot,
  type FileVersionView,
} from "../history/file-version.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  PROJECT,
  loadedBuffer,
  seedScope,
  editorProps,
} from "../components/project-files-view-test-harness.ts";
import {
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
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { applyFilesBufferLoadError, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { FilesEditor } from "./FilesEditor.tsx";

describe("FilesEditor", () => {
  setupFilesEditorTests();

  it("keeps the current editor painted until historical rows publish", async () => {
    const buffer = loadedBuffer({ path: "a.ts", content: "current contents\n" });
    const fixture = sourceReaderFixture();
    let release!: () => void;
    const held = new Promise<void>(resolve => { release = resolve; });
    const rows = vi.fn(async (...args: Parameters<typeof fixture.getSourceViewRows>) => {
      await held;
      return fixture.getSourceViewRows(...args);
    });
    const client = stubClient({ ...fixture.methods, getSourceViewRows: rows });
    const [version, setVersion] = createSignal<FileVersionView | null>(null);
    render(() => <FilesEditor {...editorProps(buffer)} client={client} version={version()} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => expect(host.querySelector(".cm-editor")).not.toBeNull());
    const editor = host.querySelector(".cm-editor");
    setVersion(fileVersionFromSnapshot({ rootId: buffer.rootId, path: buffer.path, before: "", after: "historical contents\n" }));
    await waitFor(() => expect(rows).toHaveBeenCalled());
    expect(host.parentElement!.hidden).toBe(false);
    expect(host.parentElement!.inert).toBe(true);
    expect(host.querySelector(".cm-editor")).toBe(editor);
    release();
    await waitFor(() => expect(host.parentElement!.hidden).toBe(true));
    expect(screen.getByText(/historical contents/)).toBeTruthy();
    setVersion(null);
    expect(host.parentElement!.hidden).toBe(false);
    expect(host.querySelector(".cm-editor")).toBe(editor);
  });

  it("renders a deleted file's Current state with its retained history", () => {
    const loaded = loadedBuffer();
    const buf = { ...loaded, loadError: "Source file not found" };
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        versionHistory={{
          status: "ready",
          gitStatus: "ready",
          current: { state: "absent" },
          commits: [],
          arrivals: [],
          gitHistoryState: "available",
          trackedSince: null,
          nextVersionsCursor: null,
          nextGitCursor: null,
          loadingMore: false,
          versions: [
            {
              id: "delete-1",
              file_id: "file-delete",
              workspace_kind: "project",
              operation_id: "operation-delete-1",
              effect_id: "effect-delete-1",
              root_id: "r1",
              path: buf.path,
              state: "absent",
              size_bytes: 0,
              landing: "working_file",
              capture_state: "not_applicable",
              op: "delete",
              origin: "agent",
              turn: 1,
              ordinal: 1,
              cause: "tool",
              capture_quality: "exact",
              created_at: "2026-08-23T10:00:00Z",
            },
          ],
        }}
      />
    ));

    expect(screen.getByTestId("file-current-absent").textContent).toContain(
      "This file has been deleted.",
    );
    expect(screen.getByTestId("files-editor-mode").textContent).toBe("Deleted");
    expect(
      screen.getByTestId("file-version-trigger").getAttribute("aria-label"),
    ).toBe("File version: Current");
  });

  it("does not offer restore for the current file", () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    expect(
      screen.queryByRole("button", { name: "Restore comparison baseline" }),
    ).toBeNull();
    expect(screen.queryByTestId("files-editor-restore-version")).toBeNull();
  });

  it("shows historical identity and offers icon-only restore", () => {
    const buf = loadedBuffer();
    const onRestoreVersion = vi.fn();
    const onSelectCurrent = vi.fn();
    const version = { versionId: "version-old",
fileId: "file-main",
rootId: "r1",
path: buf.path,
op: "write" as const,
ts: "2026-08-23T10:00:00Z",
beforeAvailability: "available" as const,
sha256: "older-sha",
sizeBytes: 6,
availability: "available" as const, source: { kind: "text" as const, before: "oldest\n", after: "older\n" } };
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        version={version}
        onRestoreVersion={onRestoreVersion}
        versionRestoreDisabledReason={null}
        onSelectCurrent={onSelectCurrent}
      />
    ));
    const mode = screen.getByTestId("files-editor-mode");
    expect(mode.tagName).toBe("SPAN");
    expect(mode.getAttribute("data-editor-identity")).toBe("historical-version");
    expect(mode.textContent).toBe("Historical version");
    fireEvent.click(mode);
    expect(onSelectCurrent).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("button", { name: "Restore comparison baseline" }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Restore this version" }),
    );
    expect(onRestoreVersion).toHaveBeenCalledOnce();
    expect(onRestoreVersion).toHaveBeenCalledWith(version);
    expect(screen.getByTestId("files-editor-restore-version").textContent).toBe("");
  });

  it("paints the historical version's own secret spans", async () => {
    const secret = "AKIA" + "QYJK5T" + "XV4NZR7SGB";
    const content = `key=${secret}\n`;
    const buf = loadedBuffer({ content: "current\n" });
    const fixture = sourceReaderFixture();
    const reference = fixture.prepare(null, content, "archive.json");
    fixture.setRows(reference, [{ index: 0, end: 1, kind: "insert", text: content, before_line: 0, after_line: 1, changed: [],
      secret_screen: { truncated: false, screened_bytes: content.length, spans: [{ start: 4, end: 24, state: "detected", rule_id: "aws" }] } }]);
    const { container } = render(() => (
      <FilesEditor
        {...editorProps(buf)}
        client={stubClient(fixture.methods)}
        version={{ versionId: "version-secret",
fileId: "file-main",
rootId: "r1",
path: "archive.json",
op: "write",
ts: "2026-08-23T10:00:00Z",
beforeAvailability: "absent",
sha256: "secret-sha",
sizeBytes: content.length,
availability: "available",
secretScreen: {
            truncated: false,
            screened_bytes: content.length,
            spans: [{
              start: 4,
              end: 24,
              state: "detected",
              rule_id: "aws",
            }],
          }, source: { kind: "reader" as const, reader: reference }, initialComparison: "before" }}
      />
    ));

    await waitFor(() => {
      expect(
        container.querySelector(".cm-den-secret--detected")?.textContent,
      ).toBe(secret);
    });
    expect(screen.queryByTestId("files-editor-secret-gap-chip")).toBeNull();
    expect(
      container.querySelector(".den-files-editor__status-language")?.textContent,
    ).toBe("JSON");
    expect(
      container.querySelector(".den-files-editor__status-bytes"),
    ).toBeNull();
  });

  it("renders a historical screen gap like the other status facts", () => {
    const buf = loadedBuffer();
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        version={{ versionId: "version-unscreened",
fileId: "file-main",
rootId: "r1",
path: buf.path,
op: "write",
ts: "2026-08-23T10:00:00Z",
beforeAvailability: "absent",
sha256: "older-sha",
sizeBytes: 6,
availability: "available", source: { kind: "text" as const, before: "", after: "older\n" } }}
      />
    ));

    const gap = screen.getByTestId("files-editor-secret-gap-chip");
    expect(gap.textContent).toBe("Not screened");
    expect(gap.className).toBe("den-files-editor__status-detail");
  });

  it("holds an open unread baseline when another view acknowledges the file", async () => {
    const buf = loadedBuffer({ content: "new\n" });
    await seedScope(PROJECT, [{ rootId: "r1", path: buf.path, changeId: "recent-edit", sha: buf.baseSha256!, op: "write" }]);
    const compare = vi.fn().mockResolvedValue({
      in_range: true, location_changed: false, file_id: "file-src/main.ts", presentation_after_ordinal: 2,
      before: { state: "content", availability: "available", size_bytes: 4, content: "old\n" },
      after: { state: "content", availability: "available", content: "new\n", sha256: buf.baseSha256!, size_bytes: 4 },
    });
    const [active, setActive] = createSignal(true);
    const { container } = render(() => <ResidentPresenceProvider presence={active() ? "active" : "idle"}>
      <FilesEditor {...editorProps(buf)} client={stubClient({ getProjectSourceComparison: compare })} />
    </ResidentPresenceProvider>);
    await waitFor(() => expect(container.querySelector(".cm-changedLine")).toBeTruthy());
    await seedScope(PROJECT, []);
    await waitFor(() => expect(compare).toHaveBeenLastCalledWith(PROJECT,
      expect.objectContaining({ fileId: "file-src/main.ts", presentationAfterOrdinal: 2 }), expect.anything()));
    expect(container.querySelector(".cm-changedLine")).toBeTruthy();
    setActive(false);
    await waitFor(() => expect(container.querySelector(".cm-changedLine")).toBeNull());
    setActive(true);
    await waitFor(() => expect(container.querySelector(".cm-changedLine")).toBeNull());
  });

  it("uses absent comparison endpoints even when listed history omits creation", async () => {
    const content = "export const first = 1;\nexport const second = 2;\n";
    const buf = loadedBuffer({ content });
    await seedScope(PROJECT, [{ rootId: "r1", path: buf.path, changeId: "recent-edit", sha: buf.baseSha256!, op: "write" }]);
    const compare = vi.fn(async () => ({
      in_range: true, location_changed: false,
      before: { state: "absent", availability: "absent", size_bytes: 0 },
      after: { state: "content", availability: "available", content, sha256: buf.baseSha256!, size_bytes: content.length },
    }));
    const { container } = render(() => <FilesEditor {...editorProps(buf)} client={stubClient({ getProjectSourceComparison: compare })} />);
    await waitFor(() => expect(compare).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByTestId("files-editor-host").textContent).toContain("export const second = 2;"));
    expect(container.querySelector(".cm-changedLine, .cm-changedText, .cm-deletedChunk")).toBeNull();
  });

  it("shows a version that created the file as the file, untinted and unfolded", async () => {
    const content = "a\nb\nc\nd\ne\nf\ng\nh\n";
    const showVersion = (before: { content: string; availability: "absent" | "available" }) =>
      render(() => (
        <FilesEditor
          {...editorProps(loadedBuffer())}
          client={stubClient(sourceReaderFixture().methods)}
          version={{ versionId: "version-created",
fileId: "file-main",
rootId: "r1",
path: "src/main.ts",
op: before.availability === "absent" ? "create" : "write",
ts: "2026-08-23T10:00:00Z",
beforeAvailability: before.availability,
sha256: "created-sha",
sizeBytes: content.length,
availability: "available",
initialComparison: "before", source: { kind: "text" as const, before: before.content, after: content } }}
        />
      ));

    const edited = showVersion({ content: "a\nb\nc\nd\ne\nf\ng\n", availability: "available" });
    await waitFor(() => {
      expect(edited.container.querySelector(".cm-changedLine")).toBeTruthy();
    });
    edited.unmount();

    const { container } = showVersion({ content: "", availability: "absent" });
    const host = await screen.findByTestId("file-version-file");
    await waitFor(() => {
      expect(host.querySelectorAll(".cm-line")).toHaveLength(content.split("\n").length);
    });
    expect(container.querySelector(".cm-changedLine")).toBeNull();
    expect(container.querySelector(".cm-foldPlaceholder")).toBeNull();
  });

  it("drops condenseContext and renders deleted view when opening a deleted file", async () => {
    const key = openFilesBuffer(PROJECT, {
      rootId: "r1", rootLabel: "repo", path: "common.sh", intent: "transient", condenseContext: true,
    });
    applyFilesBufferLoadError(PROJECT, key, "Source not found");
    await seedScope(PROJECT, [{
      rootId: "r1",
      path: "common.sh",
      changeId: "c-del",
      deleted: true,
      op: "delete",
    }]);

    const compare = vi.fn(async () => ({
      before: { availability: "available" as const, content: "#!/bin/sh\necho hi\n" },
      after: { availability: "absent" as const, state: "absent" as const },
    }));
    const client = stubClient({
      ...sourceReaderFixture().methods,
      getProjectSourceComparison: compare,
    });

    const preparation = createPreparation();
    render(() => (
      <PresentationProvider preparation={preparation}>
        <FilesEditor
          {...editorProps(projectFilesState(PROJECT).byKey[key]!)}
          client={client}
        />
      </PresentationProvider>
    ));

    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[key]?.condenseContext).toBe(false);
    });
    await waitFor(() => {
      expect(preparation.ready()).toBe(true);
    });
    expect(screen.getByTestId("file-current-absent")).toBeTruthy();
    expect(screen.getByText("This file has been deleted.")).toBeTruthy();
  });
});
