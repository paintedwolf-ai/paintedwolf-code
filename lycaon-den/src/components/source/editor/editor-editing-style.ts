import { completionStatus } from "@codemirror/autocomplete";
import { insertNewlineAndIndent, selectGroupForward } from "@codemirror/commands";
import { Compartment, Facet, Prec, type Extension } from "@codemirror/state";
import { EditorView, ViewPlugin, panels, type ViewUpdate } from "@codemirror/view";
import { EmacsHandler, emacsKeys } from "@replit/codemirror-emacs";
import { CodeMirror, Vim, getCM, vim } from "@replit/codemirror-vim";
import type { DenEditorKeymap } from "../../../../shared/app-state-types.ts";
import { invokeCommand } from "../../../shortcuts/dispatcher.ts";
import { editorKeymapPref } from "../../../settings/editor/editor-prefs.ts";
import { editorHistory, stepEditorHistory } from "./editor-history.ts";

const editingStyle = new Compartment();
const promptContainer = new Compartment();
const installedStyle = Facet.define<DenEditorKeymap, DenEditorKeymap>({ combine: values => values[0] ?? "emacs" });
const run = (command: string) => () => { invokeCommand(command); return true; };
let configured = false;

/** Mode commands enter the same file and history handlers as menus. */
function configureStyles(): void {
  if (configured) return;
  configured = true;
  // Explicit registration survives dependency tree shaking.
  for (const [keys, command] of Object.entries(emacsKeys)) EmacsHandler.bindKey(keys, command);
  const rotateYank = EmacsHandler.commands.yankRotate;
  EmacsHandler.commands.yankRotate = { ...rotateYank, exec: (handler: EmacsHandler, args: unknown) => {
    // Collaborative history lives in the document replica.
    if (handler.$data.lastCommand === "yank" && handler.view.state.facet(editorHistory)) stepEditorHistory(handler.view, "undo");
    return rotateYank.exec(handler, args);
  } };
  EmacsHandler.bindKey("Return|C-m", insertNewlineAndIndent);
  EmacsHandler.bindKey("M-@|M-S-2", { exec: (handler: EmacsHandler) => {
    handler.pushEmacsMark(undefined, true);
    selectGroupForward(handler.view);
  } });
  EmacsHandler.bindKey("C-/|C-x u|S-C--|C-z", (view: EditorView) => stepEditorHistory(view, "undo"));
  EmacsHandler.bindKey("S-C-/|S-C-x u|C--|S-C-z", (view: EditorView) => stepEditorHistory(view, "redo"));
  for (const [keys, command] of Object.entries({
    "C-x C-s": "files.save", "C-x C-f": "search.openFiles", "C-x b": "files.openFilesList",
    "C-x k": "files.closeTab", "C-x o": "go.nextRegion", "M-x": "search.openActions",
    "C-s": "find.inView", "C-r": "find.inView", "M-C-s": "find.next", "M-C-r": "find.prev",
    "M-g g": "editor.goToLine", "M-g M-g": "editor.goToLine", "S-M-5": "find.replace",
  })) EmacsHandler.bindKey(keys, run(command));
  CodeMirror.commands.undo = cm => stepModalHistory(cm, "undo");
  CodeMirror.commands.redo = cm => stepModalHistory(cm, "redo");
  CodeMirror.commands.save = run("files.save");
  Vim.defineEx("write", "w", run("files.save"));
  Vim.defineEx("quit", "q", run("files.closeTab"));
  Vim.defineEx("wq", "wq", run("files.saveAndClose"));
  Vim.defineEx("xit", "x", run("files.saveAndClose"));
  Vim.defineEx("bnext", "bn", run("files.nextTab"));
  Vim.defineEx("bprevious", "bp", run("files.prevTab"));
  Vim.defineEx("buffers", "buffers", run("files.openFilesList"));
  Vim.defineEx("ls", "ls", run("files.openFilesList"));
}

function stepModalHistory(cm: CodeMirror, direction: "undo" | "redo"): void {
  if (cm.curOp) cm.curOp.$changeStart = undefined;
  stepEditorHistory(cm.cm6, direction);
  const start = cm.curOp?.$changeStart;
  if (start != null) cm.cm6.dispatch({ selection: { anchor: start } });
}

// Command handling leaves caret and panel rendering to the editor.
const emacsCommands = ViewPlugin.fromClass(class {
  readonly handler: EmacsHandler;
  constructor(view: EditorView) { this.handler = new EmacsHandler(view); }
  update(update: ViewUpdate): void {
    if (update.docChanged) {
      this.handler.$emacsMark = null;
      this.handler.updateMarksOnChange(update.changes);
    }
  }
}, { eventHandlers: {
  keydown(event, view) {
    if (view.state.readOnly || event.isComposing || view.composing || event.keyCode === 229) return false;
    if ((event.key === "Enter" || event.key === "Tab") && completionStatus(view.state)) return false;
    return !!this.handler.handleKeyboard(event);
  },
  mousedown() { this.handler.$emacsMark = null; },
} });

const modeChanged = "den-editor-mode-changed";
const modeStatus = ViewPlugin.fromClass(class {
  readonly cm: CodeMirror | null;
  readonly changed: () => void;
  readonly dialogChanged: () => void;
  constructor(readonly view: EditorView) {
    this.changed = () => view.dom.dispatchEvent(new Event(modeChanged));
    this.cm = getCM(view);
    this.dialogChanged = () => this.cm?.state.dialog?.querySelector("input")?.setAttribute("aria-label", "Editor command or search");
    this.cm?.on("vim-mode-change", this.changed);
    this.cm?.on("dialog", this.dialogChanged);
    queueMicrotask(this.changed);
  }
  destroy(): void {
    this.cm?.off("vim-mode-change", this.changed);
    this.cm?.off("dialog", this.dialogChanged);
    queueMicrotask(this.changed);
  }
});

export function observeEditorEditingStatus(view: EditorView, update: (label: string) => void): () => void {
  const changed = () => {
    const state = getCM(view)?.state.vim;
    update(!state ? "" : state.insertMode ? "Insert" : state.visualMode
      ? state.visualBlock ? "Visual block" : state.visualLine ? "Visual line" : "Visual" : "Normal");
  };
  view.dom.addEventListener(modeChanged, changed);
  changed();
  return () => view.dom.removeEventListener(modeChanged, changed);
}

function extensions(style: DenEditorKeymap): Extension {
  configureStyles();
  return [installedStyle.of(style), Prec.high(style === "vim" ? [vim(), modeStatus, EditorView.theme({
    ".cm-vimMode .cm-cursorLayer:not(.cm-vimCursorLayer)": { display: "block" },
    ".cm-vimCursorLayer": { display: "none" },
  })] : emacsCommands)];
}

export function editorEditingStyle(): Extension {
  return [editingStyle.of(extensions(editorKeymapPref())), promptContainer.of([])];
}

export function applyEditorEditingStyle(view: EditorView, style: DenEditorKeymap, tabMovesFocus: boolean): void {
  const installed = editingStyle.get(view.state);
  if (installed === undefined) return;
  if (view.state.facet(installedStyle) !== style) view.dispatch({ effects: editingStyle.reconfigure(extensions(style)) });
  view.setTabFocusMode(tabMovesFocus);
}

export function setEditorPromptContainer(view: EditorView, container: HTMLElement): void {
  if (promptContainer.get(view.state) === undefined) return;
  view.dispatch({ effects: promptContainer.reconfigure(panels({ bottomContainer: container })) });
}
