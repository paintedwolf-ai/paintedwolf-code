import { contextAction, type ContextActionHandler } from "./context-actions.ts";
/** Cut / Copy / Paste / Select all, plus Add to chat when a selection exists. */

import type { ContextMenuItem } from "./ContextMenu.tsx";
import { addSelectedTextToChat } from "../chat/composer/add-to-chat.ts";
import {
  copyEditable,
  copySelectionText,
  cutEditable,
  editableHasSelection,
  pasteEditable,
  selectAllAroundTarget,
  selectAllEditable,
  selectedTextFromSnapshot,
  type TextEditContext,
} from "../platform/interaction/text-edit-context.ts";

function addToChatItem(text: string, target: EventTarget | null): ContextMenuItem {
  return contextAction("addToChat", {
    testId: "text-edit-add-to-chat",
    onSelect: () => {
      return addSelectedTextToChat(text, target);
    },
  });
}

/** System edit quartet — Cut / Copy / Paste / Select all. */
function systemEditItems(opts: {
  canMutate: boolean;
  hasSelection: boolean;
  onCut: ContextActionHandler;
  onCopy: ContextActionHandler;
  onPaste: ContextActionHandler;
  onSelectAll: ContextActionHandler;
  /** When false, Cut and Paste are omitted rather than shown disabled. */
  includeMutators?: boolean;
}): ContextMenuItem[] {
  const includeMutators = opts.includeMutators !== false;
  const items: ContextMenuItem[] = [];
  if (includeMutators) {
    items.push(contextAction("cut", {
      testId: "text-edit-cut",
      disabled: !opts.canMutate || !opts.hasSelection,
      onSelect: opts.onCut,
    }));
  }
  items.push(contextAction("copy", {
    testId: "text-edit-copy",
    disabled: !opts.hasSelection,
    onSelect: opts.onCopy,
  }));
  if (includeMutators) {
    items.push(contextAction("paste", {
      testId: "text-edit-paste",
      disabled: !opts.canMutate,
      onSelect: opts.onPaste,
    }));
  }
  items.push(contextAction("selectAll", {
    testId: "text-edit-select-all",
    onSelect: opts.onSelectAll,
  }));
  return items;
}

/** System quartet without Add to chat. */
export function textEditSystemMenuItems(
  ctx: TextEditContext,
  opts?: { omitMutatorsWhenReadOnly?: boolean },
): ContextMenuItem[] {
  if (ctx.kind === "selection") {
    return systemEditItems({
      canMutate: false,
      hasSelection: true,
      includeMutators: !opts?.omitMutatorsWhenReadOnly,
      onCut: () => undefined,
      onCopy: () => {
        return copySelectionText(ctx.text);
      },
      onPaste: () => undefined,
      onSelectAll: () => {
        selectAllAroundTarget(ctx.target);
      },
    });
  }
  const snap = ctx.snapshot;
  const hasSelection = editableHasSelection(snap);
  const canMutate = !snap.readOnly;
  return systemEditItems({
    canMutate,
    hasSelection,
    includeMutators: opts?.omitMutatorsWhenReadOnly ? canMutate : true,
    onCut: () => {
      return cutEditable(snap);
    },
    onCopy: () => {
      return copyEditable(snap);
    },
    onPaste: () => {
      return pasteEditable(snap);
    },
    onSelectAll: () => {
      selectAllEditable(snap);
    },
  });
}

/** Build inventory items for a resolved text-edit context. */
export function textEditMenuItems(ctx: TextEditContext): ContextMenuItem[] {
  if (ctx.kind === "selection") {
    return [
      ...textEditSystemMenuItems(ctx),
      addToChatItem(ctx.text, ctx.target),
    ];
  }

  const snap = ctx.snapshot;
  const items = textEditSystemMenuItems(ctx);
  if (editableHasSelection(snap)) {
    items.push(addToChatItem(selectedTextFromSnapshot(snap), snap.el));
  }
  return items;
}
