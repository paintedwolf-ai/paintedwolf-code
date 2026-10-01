import {
  setupFilesEditorTests,
  openInExternalEditor,
  revealInFileManager,
  copyTextToClipboard,
  desktopRuntime,
} from "./files-editor-test-harness.ts";
import { readSourceText } from "../../test/stylesheet-source.ts";
import "../../test/document-outbox-fixture.ts";

import { stubClient } from "../../test/client-fixture.ts";
import {
  PROJECT,
  PROJECT_SOURCE_IDENTITY,
  ROOTS,
  loadedBuffer,
  editorProps,
} from "../components/project-files-view-test-harness.ts";
import {
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

import { FilesEditor } from "./FilesEditor.tsx";
import { FilesInfoCard } from "../components/FilesInfoCard.tsx";
import {
  applyFilesBufferLoad,
  openFilesBuffer,
} from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

import { resetSourceSymbolsCacheForTests } from "../source/source-symbols-cache.ts";

describe("FilesEditor", () => {
  setupFilesEditorTests();

  it("opens a find menu from right-click on the Find button", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    fireEvent.contextMenu(screen.getByTestId("files-editor-find"));
    expect(await screen.findByTestId("files-editor-find-in-file")).toBeTruthy();
    expect(screen.getByTestId("files-editor-find-next")).toBeTruthy();
    expect(screen.getByTestId("files-editor-find-everywhere")).toBeTruthy();
  });

  it("opens a go-to menu from right-click on the Go to line button", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    fireEvent.contextMenu(screen.getByTestId("files-editor-goto"));
    expect(await screen.findByTestId("files-editor-goto-line")).toBeTruthy();
    expect(screen.getByTestId("files-editor-goto-definition")).toBeTruthy();
    expect(screen.getByTestId("files-editor-goto-next-finding")).toBeTruthy();
  });

  it("copies the full path from the host-resolved workspace root", () => {
    const buf = loadedBuffer();
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        roots={ROOTS.map((root) => ({
          ...root,
          path: "/checkouts/chat",
        }))}
      />
    ));

    const copy = screen.getByTestId("files-editor-copy");
    expect(copy.getAttribute("aria-label")).toBe("Copy path");
    fireEvent.click(copy);
    expect(copyTextToClipboard).toHaveBeenCalledWith(
      "/checkouts/chat/src/main.ts",
    );
  });

  it("uses the resolved root for info-card path actions and copy variants", async () => {
    desktopRuntime.mockReturnValue(true);
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
    const resolvedRoots = ROOTS.map((root) => ({
      ...root,
      path: "/checkouts/chat",
    }));
    render(() => (
      <FilesInfoCard
        buffer={buf}
        roots={resolvedRoots}
        onInnerLayerChange={vi.fn()}
        onRevealSegment={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByTestId("files-info-copy-toolbar"));
    expect(copyTextToClipboard).toHaveBeenCalledWith(
      "/checkouts/chat/big.bin",
    );
    fireEvent.click(screen.getByTestId("open-in-button"));
    fireEvent.click(screen.getByTestId("open-in-editor"));
    expect(openInExternalEditor).toHaveBeenCalledWith(
      expect.objectContaining({
        absolutePath: "/checkouts/chat/big.bin",
        projectRoots: ["/checkouts/chat"],
      }),
    );
    fireEvent.click(screen.getByTestId("open-in-button"));
    fireEvent.click(screen.getByTestId("open-in-file-manager"));
    expect(revealInFileManager).toHaveBeenCalledWith(
      "/checkouts/chat/big.bin",
      ["/checkouts/chat"],
    );
    fireEvent.contextMenu(screen.getByTestId("files-info-copy-toolbar"));
    expect(await screen.findByTestId("files-info-copy-relative-path")).toBeTruthy();
    expect(screen.getByTestId("files-info-copy-file-name")).toBeTruthy();
    expect(screen.queryByTestId("files-info-copy-contents")).toBeNull();
    fireEvent.click(screen.getByTestId("files-info-copy-relative-path"));
    expect(copyTextToClipboard).toHaveBeenCalledWith("big.bin");
    fireEvent.contextMenu(screen.getByTestId("files-info-copy-toolbar"));
    fireEvent.click(await screen.findByTestId("files-info-copy-file-name"));
    expect(copyTextToClipboard).toHaveBeenCalledWith("big.bin");
  });

  it("reveals the one-shot line target and clears it", async () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "src/main.ts",
      revealLine: 3,
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "src/main.ts",
      content: "line1\nline2\nline3 target\nline4\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 100,
      sha256: "base-sha",
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    render(() => <FilesEditor {...editorProps(buf)} />);
    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[key]!.revealLine).toBeNull();
    });
  });

  it("takes keyboard focus when the reveal asks for it", async () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "src/main.ts",
      revealLine: 3,
      revealFocus: true,
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "src/main.ts",
      content: "line1\nline2\nline3 target\nline4\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 100,
      sha256: "base-sha",
    });
    const buf = projectFilesState(PROJECT).byKey[key]!;
    render(() => <FilesEditor {...editorProps(buf)} />);
    await waitFor(() => {
      expect(document.activeElement?.classList.contains("cm-content")).toBe(true);
    });
    expect(projectFilesState(PROJECT).byKey[key]!.revealFocus).toBe(false);
  });

  describe("breadcrumb symbol menu", () => {
    beforeEach(() => resetSourceSymbolsCacheForTests());

    async function openSymbolMenu() {
      const buf = loadedBuffer({ path: "src/outline.ts" });
      const symbolClient = stubClient({
        listProjectSourceSymbols: async () => ({
          sha256: buf.baseSha256!,
          symbols: [
            { name: "alpha", kind: "function", line: 2 },
            { name: "beta", kind: "function", line: 3 },
          ],
          truncated: false,
        }),
      });
      const props = { ...editorProps(buf), client: symbolClient };
      render(() => <FilesEditor {...props} />);
      fireEvent.click(screen.getByText("outline.ts"));
      await waitFor(() => {
        expect(screen.getAllByRole("option").length).toBe(2);
      });
      return props;
    }

    it("renders outside the breadcrumb, which clips its own overflow", async () => {
      await openSymbolMenu();
      const menu = screen.getByTestId("files-crumb-symbol-menu");
      expect(menu.closest('[data-testid="files-editor-crumb"]')).toBeNull();
      expect(menu.classList).toContain(
        "den-files-editor__symbol-menu--float",
      );
    });

    it("lets crumb segments use the toolbar's free width and fades at the clip", async () => {
      await openSymbolMenu();
      const crumb = screen.getByTestId("files-editor-crumb");
      expect(crumb.querySelector(".den-files-editor__crumb-fade")).toBeTruthy();

      const { join } = await import("node:path");
      const css = readSourceText(
        join(import.meta.dirname, "../../files-domain.css"),
        "utf8",
      );
      const segRule = css.match(
        /\.den-files-editor__crumb-seg\s*\{[^}]+\}/,
      )?.[0];
      expect(segRule).toBeTruthy();
      expect(segRule).toContain("flex-shrink: 0");
      expect(segRule).not.toMatch(/max-width/);
      expect(segRule).not.toMatch(/text-overflow/);
      expect(css).toMatch(/\.den-files-editor__crumb-fade\s*\{/);
      expect(css).toMatch(/\.den-files-editor__crumb-fade--visible\s*\{/);
    });

    it("closes on a pointer down outside the menu", async () => {
      await openSymbolMenu();
      document.body.dispatchEvent(
        new MouseEvent("pointerdown", { bubbles: true }),
      );
      await waitFor(() => {
        expect(screen.queryByTestId("files-crumb-symbol-menu")).toBeNull();
      });
    });

    it("closes on Escape raised outside the menu", async () => {
      await openSymbolMenu();
      fireEvent.keyDown(document.body, { key: "Escape" });
      await waitFor(() => {
        expect(screen.queryByTestId("files-crumb-symbol-menu")).toBeNull();
      });
    });

    it("closes when the crumb that opened it is clicked again", async () => {
      await openSymbolMenu();
      const crumb = screen.getByText("outline.ts");
      fireEvent.pointerDown(crumb);
      fireEvent.click(crumb);
      await waitFor(() => {
        expect(screen.queryByTestId("files-crumb-symbol-menu")).toBeNull();
      });
    });

    it("stays open when its own symbol list is scrolled", async () => {
      await openSymbolMenu();
      const menu = screen.getByTestId("files-crumb-symbol-menu");
      fireEvent.scroll(menu);
      await Promise.resolve();
      expect(screen.queryByTestId("files-crumb-symbol-menu")).not.toBeNull();
    });

    it("scrolls the list, not the menu, on an overlay host", async () => {
      await openSymbolMenu();
      const list = screen.getByRole("listbox");
      const viewport = list.parentElement!;
      const frame = viewport.parentElement!;
      expect(viewport.classList).toContain("den-scrollport__viewport");
      expect(frame.classList).toContain("den-files-editor__symbol-scroll");
      expect(frame.getAttribute("data-den-scrollport")).toBe("y");
      expect(viewport.children.length).toBe(1);
    });

    it("closes when a surface outside it scrolls the crumb away", async () => {
      await openSymbolMenu();
      fireEvent.scroll(document.body);
      await waitFor(() => {
        expect(screen.queryByTestId("files-crumb-symbol-menu")).toBeNull();
      });
    });

    it("filters, arrows to a row, and jumps on Enter", async () => {
      const props = await openSymbolMenu();
      const menu = screen.getByTestId("files-crumb-symbol-menu");
      fireEvent.keyDown(menu, { key: "ArrowDown" });
      fireEvent.keyDown(menu, { key: "Enter" });
      expect(props.onSymbolJump).toHaveBeenCalledWith(3);
      await waitFor(() => {
        expect(screen.queryByTestId("files-crumb-symbol-menu")).toBeNull();
      });
    });

    it("says the filter matched nothing rather than that the file has no symbols", async () => {
      await openSymbolMenu();
      fireEvent.input(screen.getByTestId("files-crumb-symbol-filter"), {
        target: { value: "zzz" },
      });
      await waitFor(() => {
        expect(screen.getByText("No symbols match that filter")).toBeTruthy();
      });
    });
  });

  it("keeps open counts out of the status bar", () => {
    const buf = loadedBuffer();
    openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "other.ts",
    });
    render(() => <FilesEditor {...editorProps(buf)} />);
    expect(screen.queryByTestId("project-files-open-count")).toBeNull();
  });

  it("assigns status details responsive priorities", () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    const status = document.querySelector(".den-files-editor__status")!;

    expect(
      status.querySelector(".den-files-editor__status-language"),
    ).toBeTruthy();
    expect(
      screen
        .getByTestId("files-editor-indent-chip")
        .classList.contains("den-files-editor__status-indent"),
    ).toBe(true);
    expect(screen.queryByTestId("files-editor-lines-toggle")).toBeNull();
    expect(screen.getByTestId("files-editor-wrap-toggle").getAttribute("aria-pressed")).toBe("false");
  });

  it("keeps identity stable while announcing an update", () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} updatedNote />);
    expect(document.querySelector(".den-files-editor__status")?.getAttribute("data-editor-identity")).toBe("editing");
    expect(screen.queryByTestId("files-editor-mode")).toBeNull();
    expect(screen.getByTestId("files-editor-status-note").textContent).toBe("Updated to latest");
  });

  it("refreshes host annotations when a scan completes without changing its ID or file bytes", async () => {
    const buf = loadedBuffer();
    const getProjectSourceAttribution = vi.fn(async () => ({ head_sha256: buf.baseSha256, intervals: [] }));
    const listCodeScans = vi.fn(async () => ({ scans: [] }));
    const client = stubClient({ getProjectSourceAttribution, listCodeScans });
    const [scanStatus, setScanStatus] = createSignal<"running" | "complete">("running");
    render(() => <FilesEditor {...editorProps(buf)} client={client} scanUpdate={{ scan_id: "scan-a", status: scanStatus() }} />);
    await waitFor(() => expect(listCodeScans).toHaveBeenCalledTimes(1));
    setScanStatus("complete");
    await waitFor(() => expect(listCodeScans).toHaveBeenCalledTimes(2));
    expect(getProjectSourceAttribution).toHaveBeenLastCalledWith(PROJECT, {
      path: buf.path, rootId: buf.rootId, sessionId: undefined,
    });
  });

  it("manages the definition picker layer and returns focus on Escape", async () => {
    const buf = loadedBuffer();
    const onInnerLayerChange = vi.fn();
    const [pickerOpen, setPickerOpen] = createSignal(false);
    const onDefinitionPickerChange = vi.fn(() => setPickerOpen(false));
    const mounted = render(() => <FilesEditor
      {...editorProps(buf)}
      definitionPicker={pickerOpen() ? { symbol: "Resolve", candidates: [], truncated: false, selected: 0 } : null}
      onInnerLayerChange={onInnerLayerChange}
      onDefinitionPickerChange={onDefinitionPickerChange}
    />);
    await waitFor(() => expect(document.activeElement?.classList.contains("cm-content")).toBe(true));
    setPickerOpen(true);
    const picker = await screen.findByTestId("files-definition-picker");
    await waitFor(() => expect(document.activeElement).toBe(picker));
    expect(onInnerLayerChange).toHaveBeenLastCalledWith(true);
    fireEvent.keyDown(picker, { key: "Escape" });
    expect(onDefinitionPickerChange).toHaveBeenCalledWith(null);
    expect(document.activeElement?.classList.contains("cm-content")).toBe(true);
    mounted.unmount();
    expect(onInnerLayerChange).toHaveBeenLastCalledWith(false);
  });

  it("shows a definition miss in the status bar", () => {
    const buf = loadedBuffer();
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        definitionNotice="No definition found for Resolve"
      />
    ));
    expect(document.querySelector(".den-files-editor__status")?.getAttribute("data-editor-identity")).toBe("editing");
    expect(screen.queryByTestId("files-editor-mode")).toBeNull();
    expect(screen.getByTestId("files-editor-status-note").textContent).toBe("No definition found for Resolve");
  });
});
