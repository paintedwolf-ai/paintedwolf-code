import { fireEvent, render } from "@solidjs/testing-library";
import axe from "axe-core";
import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { ContextMenu } from "../../components/ContextMenu.tsx";
import { seedStockFrame } from "../../contributions/stock-frame-test.ts";
import { filesContextMenuItems } from "../commands/project-files-context-menu.ts";
import { fileVersionFromSnapshot } from "../history/file-version.ts";
import { loadedBuffer } from "../components/project-files-view-test-harness.ts";
import { type FileBuffer, projectFilesState } from "../documents/files-buffer-state.ts";
import { FilesEditorHeaderChrome, createEditorToolbarMenus } from "./FilesEditorChrome.tsx";
import {
  FilesEditorContextMenus,
  FilesEditorDefinitionPicker,
  FilesEditorGitReviewPreview,
  FilesEditorOverlays,
  FilesEditorStatusDetails,
  createFilesEditorMenus,
  type DefinitionPickerState,
} from "./FilesEditorMenus.tsx";

describe("files editor context menu a11y", () => {
  it("axe is green on the editor selection menu", async () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    el.textContent = "selection";
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 9,
            range: null,
            readOnly: false,
          },
        },
      },
      {
        newFile: () => {},
        newFolder: () => {},
        rename: () => {},
        moveTo: () => {},
        duplicate: () => {},
        moveToTrash: () => {},
        copyPath: () => {},
        copyRelativePath: () => {},
        addToChat: () => {},
        collapseAllFolders: () => {},
        expandAllFolders: () => {},
        closeTab: () => {},
        closeOtherTabs: () => {},
        closeToTheRight: () => {},
        closeSavedTabs: () => {},
        closeAllTabs: () => {},
        keepOpenTab: () => {},
        tabIsPreview: () => false,
        togglePinTab: () => {},
        tabIsPinned: () => false,
        openFilesList: () => {},
        renameTab: () => {},
        trashTab: () => {},
        copyTabPath: () => {},
        copyTabRelativePath: () => {},
        addTabToChat: () => {},
        copyPathLine: () => {},
        copyRelativePathLine: () => {},
        addPathLineToChat: () => {},
        revealInTree: () => {},
        tabRevealInTree: () => ({ onSelect: () => {} }),
        editorCopyPath: () => {},
        editorCopyRelativePath: () => {},
        editorAddSelectionToChat: () => {},
        editorSymbol: {
          target: {
            kind: "symbol",
            symbol: "resolveProjectRoot",
            from: 10,
            to: 28,
            source: "pointer",
          },
          rename: () => {},
          goToDefinition: () => {},
        },
        editorSelectionVerb: () => {},
        addSelectionShortcutHint: "⌘L",
      },
    )!;

    const { container, unmount } = render(() => (
      <ContextMenu
        anchor={{ x: 40, y: 40 }}
        items={items}
        onDismiss={() => {}}
      />
    ));
    const results = await axe.run(container, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
    });
    const summary = results.violations
      .map(
        (v) =>
          `${v.id}: ${v.help} — ${v.nodes.map((n) => n.target.join(" ")).join("; ")}`,
      )
      .join("\n");
    expect(results.violations, summary || "axe violations").toEqual([]);
    unmount();
    el.remove();
  });

  it("renders and exercises FilesEditorMenus and FilesEditorHeaderChrome", () => {
    seedStockFrame();
    const baseBuffer = loadedBuffer({
      path: "src/app.ts",
      content: "const x = 1;\n",
    });
    const buffer: FileBuffer = {
      ...baseBuffer,
      mixedEol: true,
      encoding: "utf-16le" as const,
      editorConfig: { indentStyle: "spaces", indentSize: 2, endOfLine: "lf" },
      diffPreview: {
        title: "Review diff",
        detail: "1 file changed",
        version: fileVersionFromSnapshot({ rootId: baseBuffer.rootId, path: baseBuffer.path, before: "old\n", after: "new\n" }),
        walkStep: {
          kind: "outside" as const,
          key: "step-1",
          ordinal: 1,
          effects: [],
          toolCallId: null,
          label: "Outside edits",
        },
      },
    };

    const [picker, setPicker] = createSignal<DefinitionPickerState | null>({
      symbol: "resolveProjectRoot",
      candidates: [
        { root_id: "r1", path: "src/a.ts", line: 4, kind: "function", snippet: "function resolveProjectRoot()" },
        { root_id: "r1", path: "src/b.ts", line: 9, kind: "constant", snippet: "" },
      ],
      truncated: true,
      selected: 0,
    });
    const jumped: string[] = [];
    const chosenEols: string[] = [];
    const anchorBtn = document.createElement("button");
    document.body.appendChild(anchorBtn);

    const { unmount } = render(() => {
      const menus = createFilesEditorMenus({
        projectId: () => "proj-menus",
        buffer: () => buffer,
        currentView: () => undefined,
        editable: () => true,
        roots: () => [{ id: "r1", label: "root", path: "/repo" } as any],
        version: () => ({ id: "v1", number: 2 } as any),
        activeReaderHandle: () => undefined,
        activeEditorView: () => undefined,
        lineFactsOpen: () => false,
        symbolMenuOpen: () => false,
        definitionPickerOpen: () => picker() != null,
        onInnerLayerChange: () => {},
      });
      const ic = menus.indentChip(true);
      const ec = menus.eolChip(true);
      ic?.ref(anchorBtn);
      ec?.ref(anchorBtn);
      ic?.onOpen();
      ec?.onOpen();
      menus.setToolbarMenu({ kind: "find", anchor: { x: 15, y: 15 } });
      menus.setVersionComparisonMenu({ x: 20, y: 20 });

      return (
        <div>
          <FilesEditorDefinitionPicker
            picker={picker()}
            activeEditorView={() => undefined}
            currentView={() => undefined}
            hostElement={() => anchorBtn as unknown as HTMLDivElement}
            onDefinitionPickerChange={setPicker}
            onJumpDefinitionCandidate={(c) => jumped.push(c.path)}
          />
          <FilesEditorGitReviewPreview projectId="proj-menus" buffer={buffer} />
          <FilesEditorStatusDetails
            historicalActive={false}
            buffer={buffer}
            version={null}
            secretScreenGap="truncated"
          />
          <FilesEditorStatusDetails
            historicalActive={false}
            buffer={buffer}
            version={null}
            secretScreenGap="not_screened"
          />
          <FilesEditorContextMenus
            projectId="proj-menus"
            buffer={buffer}
            editable={true}
            version={{ id: "v1", number: 2 } as any}
            versionComparison="current"
            onChooseLineEndings={(eol) => chosenEols.push(eol)}
            menus={menus}
          />
        </div>
      );
    });

    const getPickerEl = () => document.querySelector<HTMLElement>('[data-testid="files-definition-picker"]')!;
    expect(getPickerEl()).toBeTruthy();
    expect(document.querySelector('[data-testid="files-definition-picker-truncated"]')).toBeTruthy();
    fireEvent.keyDown(getPickerEl(), { key: "ArrowDown" });
    expect(picker()?.selected).toBe(1);
    fireEvent.keyDown(getPickerEl(), { key: "ArrowUp" });
    expect(picker()?.selected).toBe(0);
    fireEvent.keyDown(getPickerEl(), { key: "Enter" });
    expect(jumped).toEqual(["src/a.ts"]);
    const rows = document.querySelectorAll<HTMLButtonElement>('[data-testid="files-definition-picker-row"]');
    rows[1]!.click();
    expect(jumped).toEqual(["src/a.ts", "src/b.ts"]);
    fireEvent.keyDown(getPickerEl(), { key: "Escape" });
    expect(picker()).toBeNull();

    const walkBtn = document.querySelector<HTMLButtonElement>(".den-git-review__button")!;
    walkBtn.click();
    expect(projectFilesState("proj-menus").order.length).toBeGreaterThan(0);

    expect(document.querySelector('[data-testid="files-editor-encoding-chip"]')?.textContent).toBe("UTF-16 LE");
    expect(document.querySelector('[data-testid="files-editor-editorconfig-chip"]')).toBeTruthy();
    expect(document.querySelectorAll('[data-testid="files-editor-secret-gap-chip"]').length).toBe(2);

    expect(document.querySelector('[data-testid="files-indent-hint"]')).toBeTruthy();
    expect(document.querySelector('[data-testid="files-eol-hint"]')).toBeTruthy();
    document.querySelector<HTMLElement>('[data-testid="files-eol-lf"]')?.click();
    expect(chosenEols).toEqual(["lf"]);
    document.querySelector<HTMLElement>('[data-testid="file-version-compare-current"]')?.click();

    unmount();
    anchorBtn.remove();
  });

  it("exercises FilesEditorOverlays and FilesEditorHeaderChrome actions", () => {
    const buffer = loadedBuffer({ path: "dir/file.ts", content: "hello\n" });
    const anchor = document.createElement("div");
    document.body.appendChild(anchor);
    const calls: string[] = [];
    const rowEl = document.createElement("div");
    rowEl.textContent = "removed line";

    const readerHandle = {
      focus: () => calls.push("readerFocus"),
      gotoLine: async () => { calls.push("readerGoto"); },
      content: async () => "hello",
    };

    const { unmount } = render(() => {
      const toolbarMenus = createRoot(() =>
        createEditorToolbarMenus({
          setToolbarMenu: () => {},
          roots: () => [{ id: buffer.rootId, label: buffer.rootLabel, path: "/repo" } as any],
          buffer: () => buffer,
          activeReaderHandle: () => readerHandle as any,
          activeEditorView: () => undefined,
        }),
      );
      return (
        <div>
          <FilesEditorOverlays
            canRejectRemoved={true}
            lineFacts={{
              lineFactsCard: () => ({
                anchor,
                line: 1,
                path: "dir/file.ts",
                enter: false,
                facts: {
                  count: 1,
                  now: [{ key: "n1", title: "Worker", fact: "Editing", target: { kind: "worker", workerId: "w1" } } as any],
                  changed: [],
                  findings: [],
                },
              }),
              cancelHidePopover: () => calls.push("cancelHide"),
              scheduleHidePopover: () => calls.push("scheduleHide"),
              hideLineFacts: () => calls.push("hideFacts"),
              returnLineFactsFocus: () => calls.push("returnFocus"),
              activateLineFact: () => calls.push("activateFact"),
              removedPreview: () => ({
                preview: {
                  anchor: new DOMRect(10, 10, 100, 20),
                  fromLine: 2,
                  toLine: 2,
                  rows: [rowEl],
                  themeClasses: "",
                  font: {},
                },
              }),
              openRemovedInPlace: () => calls.push("openRemoved"),
              rejectRemoved: () => calls.push("rejectRemoved"),
              keepRemovedPreview: () => calls.push("keepRemoved"),
              scheduleHideRemovedPreview: () => calls.push("leaveRemoved"),
              hideRemovedPreview: () => calls.push("dismissRemoved"),
            }}
          />
          <FilesEditorHeaderChrome
            buffer={buffer}
            versionAvailability={undefined}
            versionPicker={null}
            stateAction={null}
            stateActionBusy={false}
            stateActionDisabledReason={null}
            onRunStateAction={() => {}}
            savedNotice={() => null}
            editable={true}
            saving={false}
            conflict={false}
            onDiscard={() => {}}
            onSave={() => {}}
            isMarkdown={false}
            historicalActive={false}
            mdView="code"
            onChangeMdView={() => {}}
            previewActive={false}
            activeEditorView={() => undefined}
            activeReaderHandle={() => readerHandle as any}
            toolbarMenus={toolbarMenus}
            onRevealSegment={(_r, p) => calls.push(`reveal:${p}`)}
            symbolMenuOpen={false}
            onToggleSymbolMenu={() => calls.push("toggleSymbols")}
            enclosingCrumbSymbol={{ name: "mySymbol", kind: "function", startLine: 1, endLine: 5 }}
            onRenameSymbol={(name) => calls.push(`rename:${name}`)}
          />
        </div>
      );
    });

    document.querySelector<HTMLButtonElement>(".files-line-facts__row")?.click();
    document.querySelector<HTMLButtonElement>('[data-testid="files-removed-lines-show"]')?.click();
    document.querySelector<HTMLButtonElement>('[data-testid="files-removed-lines-reject"]')?.click();
    document.querySelector<HTMLButtonElement>('[data-testid="files-editor-find"]')?.click();
    document.querySelector<HTMLButtonElement>('[data-testid="files-editor-goto"]')?.click();
    const symBtn = document.querySelector<HTMLButtonElement>('[data-testid="files-crumb-symbol-seg"]')!;
    symBtn.click();
    fireEvent.contextMenu(symBtn);

    expect(calls).toContain("activateFact");
    expect(calls).toContain("openRemoved");
    expect(calls).toContain("rejectRemoved");
    expect(calls).toContain("readerFocus");
    expect(calls).toContain("readerGoto");
    expect(calls).toContain("toggleSymbols");
    expect(calls).toContain("rename:mySymbol");

    unmount();
    anchor.remove();
  });
});

