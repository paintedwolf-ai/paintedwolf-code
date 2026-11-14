import { contextAction, bindContextAction } from "../../components/context-actions.ts";
import { type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";
import { convertIndentationCommand } from "../../components/source/editor/indent-convert.ts";
import { indentChipLabel, type IndentInfo } from "../../components/source/editor/indent-detect.ts";
import { effectiveIndent } from "./files-editor-display.ts";
import { settleFilesEditorView } from "./files-editor-host.ts";
import { applyFilesBufferDraft, setFilesBufferIndentOverride } from "../documents/project-files-buffers.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";
import { editorConfigIsActive } from "../../components/source/editor/editorconfig.ts";

export function createEditorFormatActions(options: Pick<FilesEditorScope, "projectId" | "buffer" | "currentView"> & {
  editable: () => boolean;
  indentChipEl: () => HTMLButtonElement | undefined;
  eolChipEl: () => HTMLButtonElement | undefined;
  setIndentMenu: (anchor: ContextMenuAnchor) => void;
  setEolMenu: (anchor: ContextMenuAnchor) => void;
}) {
  const indentChoices = (): IndentInfo[] => [
    { style: "spaces", width: 2 },
    { style: "spaces", width: 4 },
    { style: "spaces", width: 8 },
    { style: "tabs", width: 4 },
  ];

  const openIndentMenu = () => {
    const rect = options.indentChipEl()?.getBoundingClientRect();
    if (!rect) return;
    options.setIndentMenu({ x: rect.left, y: rect.top });
  };

  const openEolMenu = () => {
    const rect = options.eolChipEl()?.getBoundingClientRect();
    if (!rect) return;
    options.setEolMenu({ x: rect.left, y: rect.top });
  };

  const convertBufferIndent = (to: IndentInfo) => {
    const view = options.currentView();
    if (!options.editable() || !view || view.state.readOnly) return;
    const fromWidth = effectiveIndent(options.buffer()).width;
    convertIndentationCommand(to, fromWidth)(view);
    setFilesBufferIndentOverride(options.projectId(), options.buffer().key, to);
    const text = view.state.doc.toString();
    settleFilesEditorView(options.projectId(), options.buffer().key);
    applyFilesBufferDraft(options.projectId(), options.buffer().key, text);
  };

  const indentMenuItems = (): ContextMenuItem[] => {
    const current = effectiveIndent(options.buffer());
    return [
      ...indentChoices().map((choice) => (bindContextAction({
        label: indentChipLabel(choice),
        testId: `files-indent-${choice.style}-${choice.width}`,
        onSelect: () => {
          setFilesBufferIndentOverride(
            options.projectId(),
            options.buffer().key,
            choice,
          );
        },
      }))),
      { separator: true },
      contextAction("convertIndentationToSpaces", {
        testId: "files-indent-convert-spaces",
        disabled: !options.editable(),
        onSelect: () => {
          convertBufferIndent({ style: "spaces", width: current.width });
        },
      }),
      contextAction("convertIndentationToTabs", {
        testId: "files-indent-convert-tabs",
        disabled: !options.editable(),
        onSelect: () => {
          convertBufferIndent({ style: "tabs", width: current.width });
        },
      }),
    ];
  };

  return { openIndentMenu, openEolMenu, indentMenuItems };
}

export function editorConfigStatusLabel(buf: FileBuffer): string | null {
  if (!editorConfigIsActive(buf.editorConfig)) return null;
  return "EditorConfig";
}
