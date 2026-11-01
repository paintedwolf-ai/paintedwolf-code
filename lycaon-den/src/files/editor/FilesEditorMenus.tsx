import type { EditorView } from "@codemirror/view";
import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import type { ProjectRoot, SourceDefinitionCandidate } from "../../api/types.ts";
import { contextAction } from "../../components/context-actions.ts";
import { ContextMenu, type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { LineFactsCard } from "../../components/source/annotations/LineFactsCard.tsx";
import { RemovedLinesPopover } from "../../components/source/diff/RemovedLinesPopover.tsx";
import type { EolKind } from "../../components/source/editor/eol.ts";
import { indentChipLabel } from "../../components/source/editor/indent-detect.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { SECRET_SPAN_COPY } from "../../components/source/secrets/secret-span-copy.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { encodingChipLabel } from "../documents/project-files-buffer-kind.ts";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import { setSelectedVersionComparison, versionComparesBefore } from "../history/files-version-selection.ts";
import { walkPageTitle } from "../walk/WalkStepPage.tsx";
import {
  DEFINITION_PICKER_TRUNCATION_FOOTER, definitionPickerExplainer, definitionPickerHeader,
} from "./definition-jump.ts";
import {
  createEditorFormatActions, editorConfigStatusLabel, editorConfigStatusTip,
  encodingChipTip, eolChipTip, indentChipTip,
} from "./editor-format-actions.ts";
import { FilesSymbolMenu } from "./editor-symbol-navigation.tsx";
import { effectiveIndent } from "./files-editor-display.ts";
import { filesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import { createEditorToolbarMenus } from "./FilesEditorChrome.tsx";

type ToolbarMenuKind = Parameters<ReturnType<typeof createEditorToolbarMenus>["toolbarMenuItems"]>[0];
type VersionView = Parameters<typeof versionComparesBefore>[0];
export type DefinitionPickerState = {
  symbol: string;
  candidates: readonly SourceDefinitionCandidate[];
  truncated: boolean;
  selected: number;
};

export function createFilesEditorMenus(
  options: Pick<FilesEditorScope, "projectId" | "buffer" | "currentView"> & {
    editable: () => boolean;
    roots: () => readonly ProjectRoot[];
    version: () => VersionView | null;
    activeReaderHandle: () => SourceReaderHandle | undefined;
    activeEditorView: () => EditorView | undefined;
    lineFactsOpen: () => boolean;
    symbolMenuOpen: () => boolean;
    definitionPickerOpen: () => boolean;
    onInnerLayerChange: (open: boolean) => void;
  },
) {
  let indentChipEl: HTMLButtonElement | undefined;
  let eolChipEl: HTMLButtonElement | undefined;
  const [toolbarMenu, setToolbarMenu] = createSignal<{ kind: ToolbarMenuKind; anchor: ContextMenuAnchor } | null>(null);
  const [indentMenu, setIndentMenu] = createSignal<ContextMenuAnchor | null>(null);
  const [eolMenu, setEolMenu] = createSignal<ContextMenuAnchor | null>(null);
  const [versionComparisonMenu, setVersionComparisonMenu] = createSignal<ContextMenuAnchor | null>(null);

  createEffect(() =>
    options.onInnerLayerChange(
      options.lineFactsOpen() || toolbarMenu() != null || indentMenu() != null ||
        versionComparisonMenu() != null || options.symbolMenuOpen() || options.definitionPickerOpen(),
    ),
  );
  onCleanup(() => options.onInnerLayerChange(false));
  createEffect(() => { if (!options.version()) setVersionComparisonMenu(null); });

  const toolbarMenus = createEditorToolbarMenus({
    setToolbarMenu, roots: options.roots, buffer: options.buffer,
    activeReaderHandle: options.activeReaderHandle, activeEditorView: options.activeEditorView,
  });

  const { openIndentMenu, openEolMenu, indentMenuItems } = createEditorFormatActions({
    projectId: options.projectId, buffer: options.buffer, currentView: options.currentView,
    editable: options.editable, indentChipEl: () => indentChipEl, eolChipEl: () => eolChipEl,
    setIndentMenu, setEolMenu,
  });

  const indentChip = (visible: boolean) =>
    visible ? {
      label: indentChipLabel(effectiveIndent(options.buffer())),
      tip: indentChipTip(options.buffer()),
      onOpen: openIndentMenu,
      ref: (element: HTMLButtonElement) => { indentChipEl = element; },
    } : undefined;

  const eolChip = (visible: boolean) =>
    visible ? {
      label: options.buffer().mixedEol ? "Mixed EOL" : options.buffer().eol.toUpperCase(),
      tip: eolChipTip(options.buffer()),
      onOpen: openEolMenu,
      ref: (element: HTMLButtonElement) => { eolChipEl = element; },
    } : undefined;

  return {
    toolbarMenu, setToolbarMenu, indentMenu, setIndentMenu, eolMenu, setEolMenu,
    versionComparisonMenu, setVersionComparisonMenu, toolbarMenus, indentMenuItems, indentChip, eolChip,
  };
}

export function FilesEditorDefinitionPicker(props: {
  picker: DefinitionPickerState | null;
  activeEditorView: () => EditorView | undefined;
  currentView: () => EditorView | undefined;
  hostElement: () => HTMLDivElement | undefined;
  onDefinitionPickerChange: (next: DefinitionPickerState | null) => void;
  onJumpDefinitionCandidate: (c: SourceDefinitionCandidate) => void;
}) {
  return (
    <Show when={props.picker} keyed>
      {(picker) => (
        <AnchoredSurface
          class="den-files-editor__symbol-menu den-files-editor__definition-picker"
          role="listbox"
          ariaLabel={`Definitions of ${picker.symbol}`}
          testId="files-definition-picker"
          tabIndex={0}
          anchor={() => {
            const active = props.activeEditorView();
            const head = active?.state.selection.main.head;
            const coords = head === undefined ? null : active?.coordsAtPos(head);
            return coords
              ? {
                  top: coords.top, right: coords.right, bottom: coords.bottom, left: coords.left,
                  width: coords.right - coords.left, height: coords.bottom - coords.top,
                }
              : props.hostElement();
          }}
          preferredSide="bottom"
          align="start"
          onDismiss={() => props.onDefinitionPickerChange(null)}
          ref={(el) => queueMicrotask(() => el?.focus())}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.stopPropagation();
              props.onDefinitionPickerChange(null);
              props.currentView()?.focus();
              return;
            }
            if (e.key === "ArrowDown") {
              e.preventDefault();
              props.onDefinitionPickerChange({
                ...picker, selected: Math.min(picker.selected + 1, picker.candidates.length - 1),
              });
              return;
            }
            if (e.key === "ArrowUp") {
              e.preventDefault();
              props.onDefinitionPickerChange({ ...picker, selected: Math.max(picker.selected - 1, 0) });
              return;
            }
            if (e.key === "Enter") {
              e.preventDefault();
              const c = picker.candidates[picker.selected];
              if (c) props.onJumpDefinitionCandidate(c);
            }
          }}
        >
          <div class="den-files-editor__definition-header" data-testid="files-definition-picker-header">
            {definitionPickerHeader(picker.symbol, picker.candidates.length)}
          </div>
          <p class="den-files-editor__definition-explainer">{definitionPickerExplainer(picker.truncated)}</p>
          <ul class="den-files-editor__symbol-list">
            <For each={[...picker.candidates]}>
              {(c, i) => (
                <li>
                  <button
                    type="button"
                    class="den-files-editor__symbol-row den-files-editor__definition-row"
                    classList={{ "den-files-editor__symbol-row--active": i() === picker.selected }}
                    role="option"
                    aria-selected={i() === picker.selected}
                    data-testid="files-definition-picker-row"
                    onClick={() => props.onJumpDefinitionCandidate(c)}
                  >
                    <span class="den-files-editor__definition-row-meta">
                      <span class="den-files-editor__symbol-kind">{formatSentenceCase(c.kind)}</span>
                      <span class="den-files-editor__symbol-path">{c.path}</span>
                      <span class="den-files-editor__symbol-line">:{c.line}</span>
                    </span>
                    <Show when={c.snippet}>
                      <span class="den-files-editor__definition-row-snippet" data-testid="files-definition-picker-snippet">
                        {c.snippet}
                      </span>
                    </Show>
                  </button>
                </li>
              )}
            </For>
          </ul>
          <Show when={picker.truncated}>
            <p class="den-files-editor__definition-footer" data-testid="files-definition-picker-truncated">
              {DEFINITION_PICKER_TRUNCATION_FOOTER}
            </p>
          </Show>
        </AnchoredSurface>
      )}
    </Show>
  );
}

export function FilesEditorGitReviewPreview(props: { projectId: string; buffer: FileBuffer }) {
  return (
    <Show when={props.buffer.diffPreview?.title}>
      <div class="den-git-review__preview" data-testid="git-review-preview">
        <Show when={props.buffer.diffPreview?.walkStep}>
          {(step) => (
            <button
              type="button"
              class="den-git-review__button"
              onClick={() =>
                openFilesBuffer(props.projectId, {
                  kind: "walk", walkStep: step(), walkGitSelection: props.buffer.diffPreview?.walkSelection,
                  name: walkPageTitle(step()), rootId: "", rootLabel: "", path: "",
                  intent: "permanent", origin: "reader",
                })
              }
            >
              Back to Git review
            </button>
          )}
        </Show>
        <strong>{props.buffer.diffPreview?.title}</strong>
        <span>{props.buffer.diffPreview?.detail}</span>
      </div>
    </Show>
  );
}

export function FilesEditorOverlays(props: {
  lineFacts: {
    lineFactsCard: () => Parameters<typeof LineFactsCard>[0]["state"];
    cancelHidePopover: () => void;
    scheduleHidePopover: () => void;
    hideLineFacts: () => void;
    returnLineFactsFocus: () => void;
    activateLineFact: Parameters<typeof LineFactsCard>[0]["onActivate"];
    removedPreview: () => { preview: NonNullable<Parameters<typeof RemovedLinesPopover>[0]["preview"]> } | null;
    openRemovedInPlace: () => void;
    rejectRemoved: () => void;
    keepRemovedPreview: () => void;
    scheduleHideRemovedPreview: () => void;
    hideRemovedPreview: () => void;
  };
  canRejectRemoved: boolean;
}) {
  return (
    <>
      <LineFactsCard
        state={props.lineFacts.lineFactsCard()}
        onKeepOpen={props.lineFacts.cancelHidePopover}
        onLeave={props.lineFacts.scheduleHidePopover}
        onDismiss={props.lineFacts.hideLineFacts}
        onReturnFocus={props.lineFacts.returnLineFactsFocus}
        onActivate={props.lineFacts.activateLineFact}
      />
      <RemovedLinesPopover
        preview={props.lineFacts.removedPreview()?.preview ?? null}
        canReject={props.canRejectRemoved}
        onShowInPlace={props.lineFacts.openRemovedInPlace}
        onReject={props.lineFacts.rejectRemoved}
        onKeepOpen={props.lineFacts.keepRemovedPreview}
        onLeave={props.lineFacts.scheduleHideRemovedPreview}
        onDismiss={props.lineFacts.hideRemovedPreview}
      />
    </>
  );
}

export { FilesSymbolMenu };

export function FilesEditorStatusDetails(props: {
  historicalActive: boolean;
  buffer: FileBuffer;
  version: VersionView | null;
  secretScreenGap: "not_screened" | "truncated" | null;
}) {
  return (
    <>
      <Show when={!props.historicalActive && encodingChipLabel(props.buffer.encoding)} keyed>
        {(label) => (
          <span
            class="den-files-editor__status-detail"
            data-testid="files-editor-encoding-chip"
            data-tip={encodingChipTip(props.buffer)}
          >
            {label}
          </span>
        )}
      </Show>
      <Show when={!props.historicalActive && editorConfigStatusLabel(props.buffer)} keyed>
        {(label) => (
          <span
            class="den-files-editor__status-detail"
            data-testid="files-editor-editorconfig-chip"
            data-tip={editorConfigStatusTip(props.buffer)}
          >
            {label}
          </span>
        )}
      </Show>
      <Show when={props.secretScreenGap} keyed>
        {(gap) => (
          <span
            class="den-files-editor__status-detail"
            data-testid="files-editor-secret-gap-chip"
            data-tip={
              gap === "truncated"
                ? SECRET_SPAN_COPY.chipTruncatedTip(
                    (props.historicalActive
                      ? props.version?.secretScreen
                      : filesEditorSecretScreen(props.buffer.key)
                    )?.screened_bytes ?? 0,
                  )
                : SECRET_SPAN_COPY.chipNotScreenedTip
            }
          >
            {gap === "truncated" ? SECRET_SPAN_COPY.chipTruncated : SECRET_SPAN_COPY.chipNotScreened}
          </span>
        )}
      </Show>
    </>
  );
}

export function FilesEditorContextMenus(props: {
  projectId: string;
  buffer: FileBuffer;
  editable: boolean;
  version: VersionView | null;
  versionComparison: DenEditorVersionComparison;
  onChooseLineEndings: ((eol: EolKind) => void) | undefined;
  menus: ReturnType<typeof createFilesEditorMenus>;
}) {
  return (
    <>
      <Show when={props.menus.toolbarMenu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={props.menus.toolbarMenus.toolbarMenuItems(menu.kind)}
            onDismiss={() => props.menus.setToolbarMenu(null)}
          />
        )}
      </Show>
      <Show when={props.menus.indentMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            side="top"
            gap={4}
            items={[
              { label: "Applies to new edits in this file", disabled: true, testId: "files-indent-hint" } satisfies ContextMenuItem,
              ...props.menus.indentMenuItems(),
            ]}
            onDismiss={() => props.menus.setIndentMenu(null)}
          />
        )}
      </Show>
      <Show when={props.menus.eolMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            side="top"
            gap={4}
            items={[
              ...(props.buffer.mixedEol
                ? [{ label: "Mixed endings will normalize on save", disabled: true, testId: "files-eol-hint" } satisfies ContextMenuItem]
                : []),
              contextAction("eolLf", {
                testId: "files-eol-lf",
                disabled: !props.editable || !props.onChooseLineEndings,
                onSelect: () => props.onChooseLineEndings?.("lf"),
              }),
              contextAction("eolCrlf", {
                testId: "files-eol-crlf",
                disabled: !props.editable || !props.onChooseLineEndings,
                onSelect: () => props.onChooseLineEndings?.("crlf"),
              }),
            ]}
            onDismiss={() => props.menus.setEolMenu(null)}
          />
        )}
      </Show>
      <Show when={props.menus.versionComparisonMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            side="top"
            gap={4}
            items={[
              {
                label: "Compare selected version with",
                description: "Current shows changes if restored. Before shows what this version changed.",
                header: true,
              },
              contextAction("currentVersion", {
                checked: props.versionComparison === "current",
                testId: "file-version-compare-current",
                onSelect: () => setSelectedVersionComparison(props.projectId, props.buffer.key, "current"),
              }),
              contextAction("previousVersion", {
                checked: props.versionComparison === "before",
                disabled: !props.version || !versionComparesBefore(props.version),
                testId: "file-version-compare-before",
                onSelect: () => setSelectedVersionComparison(props.projectId, props.buffer.key, "before"),
              }),
            ]}
            onDismiss={() => props.menus.setVersionComparisonMenu(null)}
          />
        )}
      </Show>
    </>
  );
}
