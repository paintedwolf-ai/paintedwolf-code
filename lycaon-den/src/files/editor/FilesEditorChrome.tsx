import { editorWordWrapPref, saveEditorWordWrap } from "../../settings/editor/editor-prefs.ts";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { Show, createSignal, onCleanup, type JSX } from "solid-js";
import type { EditorView } from "@codemirror/view";
import type { ProjectRoot } from "../../api/types.ts";
import { bindChromeContextMenu } from "../../components/context-menu-open.ts";
import type { ContextMenuAnchor, ContextMenuItem } from "../../components/ContextMenu.tsx";
import { copyPathMenuItems } from "../../components/copy-path-menu-items.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { openEditorFind, openEditorGotoLine, openEditorReplace } from "../../components/source/editor/codemirror-theme.ts";
import { openFind } from "../../find/find-controller.ts";
import { findBarDisclosure, gotoLineBarDisclosure } from "../../find/command-bar-disclosure.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { absolutePathForBuffer } from "../components/project-files-model.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { filesEditorFindMenuItems, filesEditorGotoMenuItems, type FilesEditorToolbarMenuKind } from "./files-editor-toolbar-menus.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import type { editorStateAction, resolveEditorIdentity } from "../documents/editor-identity.ts";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { BrowseSegmented } from "../../components/browse/BrowseSegmented.tsx";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { CheckIcon, CopyIcon, FindIcon, GotoLineIcon } from "../../components/source/viewer-icons.tsx";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { FilesPathCrumbs } from "./FilesPathCrumbs.tsx";

export function FilesEditorToolbar(props: { children: JSX.Element; actions: JSX.Element }) {
  return <div class="den-files-toolbar">
    {props.children}
    <span class="den-files-editor__toolbar-actions">{props.actions}</span>
  </div>;
}

type StatusChip = { label: string; tip?: string; onOpen?: () => void; ref?: (element: HTMLButtonElement) => void };
export type FilesEditorStatusProps = {
  identity: ReturnType<typeof resolveEditorIdentity>;
  windows?: JSX.Element;
  notices?: JSX.Element;
  dirtyCount: number;
  cursor?: { line: number; col: number };
  language: string;
  indent?: StatusChip;
  eol?: StatusChip;
  details?: JSX.Element;
  simplified?: boolean;
};

function StatusChipView(props: { chip: StatusChip; testId: string; class?: string }) {
  return <Show when={props.chip.onOpen} fallback={<span class="den-files-editor__status-detail" data-testid={props.testId} data-tip={props.chip.tip}>{props.chip.label}</span>}>
    <button type="button" ref={element=>props.chip.ref?.(element)} class={`den-files-editor__status-toggle ${props.class ?? ""}`} data-testid={props.testId}
      aria-haspopup="menu" data-tip={props.chip.tip} onClick={()=>props.chip.onOpen?.()}>{props.chip.label}</button>
  </Show>;
}

export function FilesEditorStatus(props: FilesEditorStatusProps) {
  return <div class="den-files-editor__status" data-files-ctx="no-menu" data-editor-identity={props.identity.id}>
    {props.windows}
    <Show when={props.identity.id !== "editing"}><span class="den-files-editor__mode" classList={{"den-files-editor__mode--readonly":props.identity.readOnly}}
      data-testid="files-editor-mode" data-editor-identity={props.identity.id} aria-live="polite">
      <ThemeIcon slot={props.identity.icon} size={13} />{props.identity.label}
    </span></Show>
    {props.notices}
    <Show when={props.dirtyCount>0}><span data-testid="files-editor-unsaved-count">{props.dirtyCount} unsaved</span></Show>
    <Show when={props.cursor}>{cursor=><span>Ln {cursor().line}, Col {cursor().col}</span>}</Show>
    <span class="den-files-editor__status-language">{props.language}</span>
    <ShowLatest when={props.indent}>{chip=><StatusChipView chip={chip()} testId="files-editor-indent-chip" class="den-files-editor__status-indent" />}</ShowLatest>
    <ShowLatest when={props.eol}>{chip=><StatusChipView chip={chip()} testId="files-editor-eol-chip" />}</ShowLatest>
    {props.details}
    <Show when={props.simplified}><span class="den-files-editor__status-detail" data-testid="files-editor-simplified-chip"
      data-tip="This file is very large or has very long lines. Wrapping, indent guides, and syntax highlighting stay off here so the editor keeps up.">Simplified</span></Show>
    <span class="den-files-editor__status-spacer" />
    <button type="button" class="den-files-editor__status-toggle den-files-editor__status-wrap"
      data-testid="files-editor-wrap-toggle" aria-pressed={editorWordWrapPref()}
      onClick={() => void saveEditorWordWrap(!editorWordWrapPref())}>
      Wrap
    </button>
  </div>;
}

/** The toolbar's find, go-to and copy menus, their buttons' command bar disclosures, and path copy. */
export function createEditorToolbarMenus(options: {
  setToolbarMenu: (menu: { kind: FilesEditorToolbarMenuKind; anchor: ContextMenuAnchor }) => void;
  roots: () => readonly ProjectRoot[];
  buffer: () => FileBuffer;
  activeReaderHandle: () => SourceReaderHandle | undefined;
  activeEditorView: () => EditorView | undefined;
}) {
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  const [copiedPath, setCopiedPath] = createSignal(false);
  const root = () => options.roots().find((r) => r.id === options.buffer().rootId);
  const absolutePath = (): string | undefined => {
    const r = root();
    return r ? absolutePathForBuffer(r.path, options.buffer().path) : undefined;
  };
  const flashCopied = () => {
    setCopiedPath(true);
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => setCopiedPath(false), 1400);
  };
  const onCopy = () => {
    const abs = absolutePath();
    if (!abs) return;
    void copyTextToClipboard(abs);
    flashCopied();
  };
  const onCopyRelative = () => {
    void copyTextToClipboard(options.buffer().path);
    flashCopied();
  };
  const onCopyFileName = () => {
    void copyTextToClipboard(options.buffer().name);
    flashCopied();
  };
  const onCopyContents = () => {
    const reader = options.activeReaderHandle();
    if (reader) {
      void reader.content().then(text => { void copyTextToClipboard(text); flashCopied(); });
      return;
    }
    const text =
      options.activeEditorView()?.state.doc.toString() ??
      filesBufferText(options.buffer());
    void copyTextToClipboard(text);
    flashCopied();
  };
  const copyMenuItems = (): ContextMenuItem[] => {
    const buffer = options.buffer();
    return copyPathMenuItems({
      copyAbsolute: absolutePath() ? onCopy : null,
      copyRelative: onCopyRelative,
      copyFileName: onCopyFileName,
      copyContents: buffer.kind === "text" && (!buffer.deleted || buffer.deleted.previous.availability === "available") ? onCopyContents : null,
      absoluteTestId: "files-editor-copy-path",
      relativeTestId: "files-editor-copy-relative-path",
      fileNameTestId: "files-editor-copy-file-name",
      contentsTestId: "files-editor-copy-contents",
    });
  };
  onCleanup(() => { if (copiedTimer) clearTimeout(copiedTimer); });
  const openToolbarMenu = (
    kind: FilesEditorToolbarMenuKind,
    anchor: ContextMenuAnchor,
  ) => options.setToolbarMenu({ kind, anchor });
  const findMenuBind = bindChromeContextMenu((anchor) =>
    openToolbarMenu("find", anchor),
  );
  const gotoMenuBind = bindChromeContextMenu((anchor) =>
    openToolbarMenu("goto", anchor),
  );
  const copyMenuBind = bindChromeContextMenu((anchor) =>
    openToolbarMenu("copy", anchor),
  );

  const toolbarMenuItems = (kind: FilesEditorToolbarMenuKind): ContextMenuItem[] => {
    switch (kind) {
      case "find":
        return filesEditorFindMenuItems({
          findInFile: () => {
            const active = options.activeEditorView();
            if (active) openEditorFind(active);
            else { options.activeReaderHandle()?.focus(); openFind(); }
          },
          replaceInFile: () => {
            const active = options.activeEditorView();
            if (active) openEditorReplace(active);
          },
          findNext: () => invokeCommand("find.next"),
          findPrev: () => invokeCommand("find.prev"),
          selectAllMatches: () => invokeCommand("find.selectAllMatches"),
          findEverywhere: () => invokeCommand("find.everywhere"),
        });
      case "goto":
        return filesEditorGotoMenuItems({
          goToLine: () => {
            const active = options.activeEditorView();
            if (active) openEditorGotoLine(active);
            else void options.activeReaderHandle()?.gotoLine();
          },
          goToDefinition: () => invokeCommand("files.goToDefinition"),
          nextFinding: () => invokeCommand("editor.nextFinding"),
          previousFinding: () => invokeCommand("editor.previousFinding"),
          showSecretScreen: () => invokeCommand("files.showSecretScreen"),
        });
      case "copy":
        return copyMenuItems();
    }
  };

  return { copiedPath, onCopy, findMenuBind, gotoMenuBind, copyMenuBind, toolbarMenuItems, findBarDisclosure, gotoLineBarDisclosure };
}

export function FilesEditorHeaderChrome(props: {
  buffer: FileBuffer;
  versionAvailability: string | undefined;
  versionPicker: JSX.Element;
  stateAction: ReturnType<typeof editorStateAction>;
  stateActionBusy: boolean | undefined;
  stateActionDisabledReason: string | null;
  onRunStateAction: () => void;
  savedNotice: (checkIcon: JSX.Element) => JSX.Element;
  editable: boolean;
  saving: boolean;
  conflict: boolean;
  onDiscard: () => void;
  onSave: () => void;
  isMarkdown: boolean;
  historicalActive: boolean;
  mdView: "code" | "preview";
  onChangeMdView: (next: "code" | "preview") => void;
  previewActive: boolean;
  activeEditorView: () => EditorView | undefined;
  activeReaderHandle: () => SourceReaderHandle | undefined;
  toolbarMenus: ReturnType<typeof createEditorToolbarMenus>;
  onRevealSegment: (rootId: string, path: string, isDir: boolean) => void;
  symbolMenuOpen: boolean;
  onToggleSymbolMenu: (trigger: HTMLElement) => void;
  enclosingCrumbSymbol: {
    name: string;
    kind: string;
    startLine: number;
    endLine: number;
  } | null;
  onRenameSymbol: ((name: string) => void) | undefined;
}) {
  return (
    <FilesEditorToolbar
      actions={
        <>
          {props.versionPicker}
          <Show when={props.stateAction} keyed>
            {(action) => (
              <DenButton
                variant={
                  action.id === "restore-version" && props.versionAvailability === "absent"
                    ? "danger"
                    : "primary"
                }
                compact
                class="den-files-editor__state-action"
                data-testid={`files-editor-${action.id}`}
                data-state-action={action.id}
                disabled={props.stateActionDisabledReason != null}
                aria-label={
                  props.stateActionBusy ? `${action.label} in progress` : action.label
                }
                data-tip={props.stateActionDisabledReason ?? action.tip}
                data-tip-pos="below"
                onClick={props.onRunStateAction}
              >
                <ThemeIcon slot={action.icon} size={14} />
              </DenButton>
            )}
          </Show>
          {props.savedNotice(<CheckIcon size={13} />)}
          <Show when={props.editable && props.buffer.dirty}>
            <DenButton
              variant="ghost"
              compact
              data-testid="files-editor-discard"
              disabled={props.saving}
              onClick={() => props.onDiscard()}
            >
              Discard
            </DenButton>
            <DenButton
              variant="primary"
              compact
              data-testid="files-editor-save"
              data-tip={`Save (${bindingForHandler("files.save")})`}
              data-tip-pos="below"
              disabled={props.saving || props.conflict}
              onClick={() => props.onSave()}
            >
              {props.saving ? "Saving…" : "Save"}
            </DenButton>
          </Show>
          <Show when={props.isMarkdown && !props.historicalActive}>
            <BrowseSegmented
              class="den-files-editor__seg"
              testId="files-editor-md-toggle"
              ariaLabel="Markdown view"
              value={props.mdView}
              onChange={(id) => {
                if (id === "preview" || id === "code") {
                  props.onChangeMdView(id);
                }
              }}
              options={[
                { id: "code", label: "Code", testId: "files-editor-md-code" },
                {
                  id: "preview",
                  label: "Preview",
                  testId: "files-editor-md-preview",
                },
              ]}
            />
          </Show>
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            data-testid="files-editor-find"
            data-tip={`Find (${bindingForHandler("find.inView")})`}
            data-tip-pos="below"
            aria-label="Find in file"
            aria-expanded={props.toolbarMenus.findBarDisclosure().expanded}
            aria-controls={props.toolbarMenus.findBarDisclosure().controls}
            disabled={
              props.buffer.loading ||
              props.previewActive ||
              (!props.activeEditorView() && !props.activeReaderHandle())
            }
            onClick={() => {
              const active = props.activeEditorView();
              if (active) openEditorFind(active);
              else {
                props.activeReaderHandle()?.focus();
                openFind();
              }
            }}
            onContextMenu={props.toolbarMenus.findMenuBind.onContextMenu}
            onKeyDown={props.toolbarMenus.findMenuBind.onKeyDown}
          >
            <FindIcon />
          </button>
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            data-testid="files-editor-goto"
            data-tip={`Go to line (${bindingForHandler("editor.goToLine")})`}
            data-tip-pos="below"
            aria-label="Go to line"
            aria-expanded={props.toolbarMenus.gotoLineBarDisclosure().expanded}
            aria-controls={props.toolbarMenus.gotoLineBarDisclosure().controls}
            disabled={
              props.buffer.loading ||
              props.previewActive ||
              (!props.activeEditorView() && !props.activeReaderHandle())
            }
            onClick={() => {
              const active = props.activeEditorView();
              if (active) openEditorGotoLine(active);
              else void props.activeReaderHandle()?.gotoLine();
            }}
            onContextMenu={props.toolbarMenus.gotoMenuBind.onContextMenu}
            onKeyDown={props.toolbarMenus.gotoMenuBind.onKeyDown}
          >
            <GotoLineIcon />
          </button>
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            classList={{ "den-files-editor__tool--ok": props.toolbarMenus.copiedPath() }}
            data-testid="files-editor-copy"
            data-tip="Copy path"
            data-tip-pos="below"
            aria-label="Copy path"
            onClick={props.toolbarMenus.onCopy}
            onContextMenu={props.toolbarMenus.copyMenuBind.onContextMenu}
            onKeyDown={props.toolbarMenus.copyMenuBind.onKeyDown}
          >
            <Show when={props.toolbarMenus.copiedPath()} fallback={<CopyIcon />}>
              <CheckIcon />
            </Show>
          </button>
        </>
      }
    >
      <FilesPathCrumbs
        rootId={props.buffer.rootId}
        rootLabel={props.buffer.rootLabel}
        path={props.buffer.path}
        testId="files-editor-crumb"
        onRevealSegment={props.onRevealSegment}
        fileSegment={(seg) => (
          <button
            type="button"
            class="den-files-editor__crumb-seg den-files-editor__crumb-file"
            data-files-ctx="breadcrumb"
            data-root={props.buffer.rootId}
            data-path={seg.path}
            data-isdir="false"
            aria-expanded={props.symbolMenuOpen}
            aria-haspopup="listbox"
            onClick={(e) => props.onToggleSymbolMenu(e.currentTarget)}
          >
            {seg.name}
          </button>
        )}
      >
        <Show when={props.enclosingCrumbSymbol}>
          {(sym) => (
            <>
              <span class="den-files-editor__crumb-sep" aria-hidden="true">
                ›
              </span>
              <button
                type="button"
                class="den-files-editor__crumb-seg den-files-editor__crumb-symbol"
                data-testid="files-crumb-symbol-seg"
                data-tip={`${sym().kind ? `${formatSentenceCase(sym().kind)} ` : ""}${sym().name} — lines ${sym().startLine}–${sym().endLine}. Click for symbols; right-click to rename.`}
                aria-expanded={props.symbolMenuOpen}
                aria-haspopup="listbox"
                onClick={(e) => props.onToggleSymbolMenu(e.currentTarget)}
                onContextMenu={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  props.onRenameSymbol?.(sym().name);
                }}
              >
                <Show when={sym().kind}>
                  <span class="den-files-editor__crumb-symbol-kind">
                    {formatSentenceCase(sym().kind)}
                  </span>
                </Show>
                {sym().name}
              </button>
            </>
          )}
        </Show>
      </FilesPathCrumbs>
    </FilesEditorToolbar>
  );
}
