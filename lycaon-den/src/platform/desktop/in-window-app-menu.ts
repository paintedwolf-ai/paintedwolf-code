import { createSignal } from "solid-js";
import { EditorView } from "@codemirror/view";
import type { ContextMenuEntry, ContextMenuItem } from "../../components/ContextMenu.tsx";
import { stepEditorHistory } from "../../components/source/editor/editor-history.ts";
import { nativeCommandId } from "../../contributions/dispatch.ts";
import { buildAppMenuSpec, type AppMenuItemSpec, type AppMenuSpec } from "../../shortcuts/app-menu-model.ts";
import { displayBinding } from "../../shortcuts/chord.ts";
import { displayBindingFor } from "../../shortcuts/display-binding-for.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import {
  copyEditable,
  cutEditable,
  editableHasSelection,
  findTextEditable,
  pasteEditable,
  selectAllEditable,
  snapshotEditable,
  type EditableSnapshot,
} from "../interaction/text-edit-context.ts";
import { isTauriRuntime } from "../runtime.ts";
import { currentWindow } from "../windows/current-window.ts";
import { windowFullscreen, windowMaximized } from "../windows/window-maximized.ts";
import { activateAppMenuCommand } from "./app-menu.ts";

/** Each keyboard request to open the menu; the title bar button consumes them. */
const [openRequests, setOpenRequests] = createSignal(0);
export const appMenuOpenRequests = openRequests;

export function requestAppMenuOpen(): void {
  setOpenRequests((count) => count + 1);
}

function contributedItem(item: AppMenuItemSpec): ContextMenuEntry {
  return {
    label: item.title,
    shortcut: item.binding ? displayBinding(item.binding) : undefined,
    disabled: !item.enabled,
    checked: item.checked,
    testId: `app-menu-item-${item.id}`,
    onSelect: () => void activateAppMenuCommand(item.id),
  };
}

function contributedGroups(section: AppMenuSpec | undefined): ContextMenuEntry[][] {
  return (section?.groups ?? [])
    .filter((group) => group.length > 0)
    .map((group) => group.map(contributedItem));
}

/** Groups joined by separators; empty groups add none. */
function joinGroups(groups: ContextMenuEntry[][]): ContextMenuEntry[] {
  const items: ContextMenuEntry[] = [];
  for (const group of groups) {
    if (group.length === 0) continue;
    if (items.length > 0) items.push({ separator: true });
    items.push(...group);
  }
  return items;
}

/** The editable the menu acts on: the one holding focus. */
function focusedEditable(): EditableSnapshot | null {
  const el = findTextEditable(document.activeElement);
  return el ? snapshotEditable(el) : null;
}

function stepHistory(direction: "undo" | "redo"): void {
  const snap = focusedEditable();
  if (!snap) return;
  const view = snap.el instanceof HTMLElement ? EditorView.findFromDOM(snap.el) : null;
  if (view) {
    stepEditorHistory(view, direction);
    return;
  }
  snap.el.focus();
  document.execCommand(direction);
}

/** Undo, redo, cut, copy, paste, select all against the focused editable. */
function editItems(): ContextMenuEntry[][] {
  const snap = focusedEditable();
  const editable = snap !== null && !snap.readOnly;
  const selected = snap !== null && editableHasSelection(snap);
  const act = (run: (snapshot: EditableSnapshot) => unknown) => () => {
    const current = focusedEditable();
    if (current) void run(current);
  };
  return [
    [
      { label: "Undo", shortcut: displayBinding("Mod+Z"), disabled: !editable, onSelect: () => stepHistory("undo") },
      { label: "Redo", shortcut: displayBinding("Mod+Shift+Z"), disabled: !editable, onSelect: () => stepHistory("redo") },
    ],
    [
      { label: "Cut", shortcut: displayBinding("Mod+X"), disabled: !editable || !selected, onSelect: act(cutEditable) },
      { label: "Copy", shortcut: displayBinding("Mod+C"), disabled: !selected, onSelect: act(copyEditable) },
      { label: "Paste", shortcut: displayBinding("Mod+V"), disabled: !editable, onSelect: act(pasteEditable) },
      { label: "Select all", shortcut: displayBinding("Mod+A"), disabled: snap === null, onSelect: act(selectAllEditable) },
    ],
  ];
}

/** A stock command's menu entry, labelled with its live chord. */
function commandItem(handlerId: string, label: string): ContextMenuEntry {
  const commandId = nativeCommandId(handlerId);
  const chord = commandId ? displayBindingFor(commandId) : "—";
  return {
    label,
    shortcut: chord === "—" ? undefined : chord,
    testId: `app-menu-item-${handlerId}`,
    onSelect: () => {
      invokeCommand(handlerId);
    },
  };
}

function windowItems(): ContextMenuEntry[] {
  if (!isTauriRuntime()) return [];
  return [
    { label: "Minimize", onSelect: () => void currentWindow().then((win) => win.minimize()) },
    {
      label: windowMaximized() ? "Restore" : "Maximize",
      onSelect: () => void currentWindow().then((win) => win.toggleMaximize()),
    },
  ];
}

function fullscreenItem(): ContextMenuEntry[] {
  if (!isTauriRuntime()) return [];
  return [
    {
      label: windowFullscreen() ? "Exit full screen" : "Enter full screen",
      onSelect: () => void currentWindow().then((win) => win.setFullscreen(!windowFullscreen())),
    },
  ];
}

/**
 * The whole menu for a window without a menu bar: the same contributed
 * sections the native bar carries, with the edit, window, and quit entries the
 * native bar adds on its own.
 */
export function inWindowAppMenuItems(): ContextMenuItem[] {
  const sections = new Map(buildAppMenuSpec().map((section) => [section.menu, section]));
  const section = (menu: AppMenuSpec["menu"], groups: ContextMenuEntry[][]): ContextMenuItem[] => {
    const items = joinGroups(groups);
    if (items.length === 0) return [];
    const spec = sections.get(menu);
    return [{ label: spec?.title ?? menu, submenu: items, testId: `app-menu-section-${menu}` }];
  };
  return [
    ...section("app", [
      ...contributedGroups(sections.get("app")),
      [commandItem("app.quit", "Quit Painted Wolf Code")],
    ]),
    ...section("file", contributedGroups(sections.get("file"))),
    ...section("edit", [...editItems(), ...contributedGroups(sections.get("edit"))]),
    ...section("selection", contributedGroups(sections.get("selection"))),
    ...section("view", [...contributedGroups(sections.get("view")), fullscreenItem()]),
    ...section("go", contributedGroups(sections.get("go"))),
    ...section("window", [
      windowItems(),
      [commandItem("window.close", "Close window")],
      ...contributedGroups(sections.get("window")),
    ]),
    ...section("help", contributedGroups(sections.get("help"))),
  ];
}
