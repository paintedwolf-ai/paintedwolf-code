import { contextAction } from "../../components/context-actions.ts";
import type { ContextMenuItem } from "../../components/ContextMenu.tsx";

export type FilesEditorToolbarMenuKind = "find" | "goto" | "copy";

export type FilesEditorFindMenuActions = {
  findInFile: () => void;
  replaceInFile: () => void;
  findNext: () => void;
  findPrev: () => void;
  selectAllMatches: () => void;
  findEverywhere: () => void;
};

export function filesEditorFindMenuItems(
  actions: FilesEditorFindMenuActions,
): ContextMenuItem[] {
  return [
    contextAction("findInFile", {
      testId: "files-editor-find-in-file",
      onSelect: actions.findInFile,
    }),
    contextAction("replaceInFile", {
      testId: "files-editor-find-replace",
      onSelect: actions.replaceInFile,
    }),
    contextAction("findNext", {
      testId: "files-editor-find-next",
      onSelect: actions.findNext,
    }),
    contextAction("findPrevious", {
      testId: "files-editor-find-prev",
      onSelect: actions.findPrev,
    }),
    contextAction("selectAllFindMatches", {
      testId: "files-editor-find-select-all",
      onSelect: actions.selectAllMatches,
    }),
    contextAction("findEverywhere", {
      testId: "files-editor-find-everywhere",
      onSelect: actions.findEverywhere,
    }),
  ];
}

export type FilesEditorGotoMenuActions = {
  goToLine: () => void;
  goToDefinition: () => void;
  nextFinding: () => void;
  previousFinding: () => void;
  /** Reports what the host screen found and which catalog found it. */
  showSecretScreen: () => void;
};

export function filesEditorGotoMenuItems(
  actions: FilesEditorGotoMenuActions,
): ContextMenuItem[] {
  return [
    contextAction("goToLine", {
      testId: "files-editor-goto-line",
      onSelect: actions.goToLine,
    }),
    contextAction("goToDefinition", {
      testId: "files-editor-goto-definition",
      onSelect: actions.goToDefinition,
    }),
    contextAction("showSecretScreen", {
      testId: "files-editor-show-secret-screen",
      onSelect: actions.showSecretScreen,
    }),
    contextAction("goToNextFinding", {
      testId: "files-editor-goto-next-finding",
      onSelect: actions.nextFinding,
    }),
    contextAction("goToPreviousFinding", {
      testId: "files-editor-goto-prev-finding",
      onSelect: actions.previousFinding,
    }),
  ];
}
