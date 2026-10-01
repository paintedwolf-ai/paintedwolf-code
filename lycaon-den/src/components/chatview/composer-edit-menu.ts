import { createSignal, type Accessor } from "solid-js";
import { contextAction } from "../context-actions.ts";
import type { ContextMenuAnchor, ContextMenuItem } from "../ContextMenu.tsx";
import type { MarkSecretTarget } from "../source/secrets/MarkSecretDialog.tsx";
import { textEditSystemMenuItems } from "../text-edit-menu-items.ts";
import { snapshotEditable, type EditableSnapshot } from "../../platform/interaction/text-edit-context.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createComposerSecretForMark, revokeComposerSecret } from "./composer-secret-mark.ts";
import { protectComposerSelection } from "../../chat/composer/composer-document-store.ts";
import type { ChatDestination } from "../../chat/composer/shared-composer-document.ts";
import type { SecretMarkPreview } from "../../api/types.ts";

type ComposerEditMenuOptions = {
  input: Accessor<HTMLTextAreaElement | undefined>;
  text: Accessor<string>;
  sessionId: Accessor<string>;
  projectId: Accessor<string>;
  destination: Accessor<ChatDestination>;
  removeMarkedDraftRange: (start: number, end: number, nextDraft: string) => void;
  growDraft: () => void;
};

export function createComposerEditMenu(options: ComposerEditMenuOptions) {
  const { text, sessionId, projectId, destination, removeMarkedDraftRange, growDraft } = options;
  const input = { get current() { return options.input(); } };
  const [editMenuAnchor, setEditMenuAnchor] = createSignal<ContextMenuAnchor | null>(null);
  const [editMenuRetainsFocus, setEditMenuRetainsFocus] = createSignal(false);
  const [markSecretTarget, setMarkSecretTarget] = createSignal<MarkSecretTarget | null>(null);
  let secretSelection: { start: number; end: number; value: string } | null = null;
  let editMenuSnapshot: EditableSnapshot | null = null;
  const selectedDraftText = () => {
    const el = input.current;
    if (!el || el.selectionStart === el.selectionEnd) return null;
    return {
      start: el.selectionStart,
      end: el.selectionEnd,
      value: el.value.slice(el.selectionStart, el.selectionEnd),
    };
  };

  const captureSelection = (
    selection: { start: number; end: number; value: string },
    trim: boolean,
  ) => {
    if (!trim) return selection;
    const leading = selection.value.match(/^\s*/u)?.[0].length ?? 0;
    const trailing = selection.value.match(/\s*$/u)?.[0].length ?? 0;
    let start = selection.start + leading;
    let end = selection.end - trailing;
    const value = text().slice(start, end);
    const last = value[value.length - 1];
    if (value.length >= 2 && ((value[0] === '"' && last === '"') ||
      (value[0] === "'" && last === "'") ||
      (value[0] === "`" && last === "`"))) {
      start += 1;
      end -= 1;
    }
    return { start, end, value: text().slice(start, end) };
  };

  const previewSecretSelection = (
    selection: { start: number; end: number; value: string },
    trim: boolean,
  ): SecretMarkPreview => {
    const captured = captureSelection(selection, trim);
    const runeLength = Array.from(captured.value).length;
    return {
      eligible: runeLength > 0,
      ...(runeLength > 0 ? {} : { reason: "empty" as const }),
      start: captured.start,
      end: captured.end,
      rune_length: runeLength,
      byte_length: new TextEncoder().encode(captured.value).length,
      shape: `${runeLength} ${runeLength === 1 ? "character" : "characters"}`,
      trimmed_leading: captured.start - selection.start,
      trimmed_trailing: selection.end - captured.end,
    };
  };

  const beginMarkSecret = () => {
    const selection = secretSelection;
    setEditMenuAnchor(null);
    if (!selection || text().slice(selection.start, selection.end) !== selection.value) return;
    setMarkSecretTarget({
      path: "Message draft",
      line: 1,
      dirty: true,
      preview: async (trim) => previewSecretSelection(selection, trim),
      mark: async ({ name, purpose, trim }) => {
        if (text().slice(selection.start, selection.end) !== selection.value) {
          throw new Error("The selected draft changed. Select it again.");
        }
        const client = getLycaonClient();
        if (!client) throw new Error("The app backend is unavailable.");
        const captured = captureSelection(selection, trim);
        if (!captured.value) throw new Error("Select at least one character.");
        const metadata = await createComposerSecretForMark(
          client,
          sessionId(),
          projectId(),
          {
            name,
            purpose,
            secret_value: captured.value,
            operation_id: crypto.randomUUID(),
          },
        );
        const nextDraft = `${text().slice(0, captured.start)}${text().slice(captured.end)}`;
        try {
          await protectComposerSelection(destination(), nextDraft, {
            id: crypto.randomUUID(),
            kind: "secret",
            name: metadata.name,
            projectId: projectId(),
            reference: metadata.reference,
            scope: metadata.scope,
            shape: previewSecretSelection(selection, trim).shape,
            runeLength: Array.from(captured.value).length,
          });
        } catch (error) {
          // Marking failure revokes the capability.
          await revokeComposerSecret(client, projectId(), metadata.reference);
          throw error;
        }
        removeMarkedDraftRange(captured.start, captured.end, nextDraft);
        queueMicrotask(() => {
          if (!input.current) return;
          input.current.setSelectionRange(captured.start, captured.start);
          growDraft();
        });
        return metadata.name;
      },
    });
  };

  const editContextMenuItems = (): ContextMenuItem[] => {
    const snapshot = editMenuSnapshot;
    if (!snapshot) return [];
    const system = textEditSystemMenuItems({ kind: "editable", snapshot });
    if (!secretSelection) return system;
    return [
      contextAction("markAsSecret", {  testId: "composer-mark-secret", onSelect: beginMarkSecret }),
      { separator: true },
      ...system,
    ];
  };

  const openEditContextMenu = (event?: MouseEvent) => {
    const el = input.current;
    if (!el) return false;
    secretSelection = selectedDraftText();
    editMenuSnapshot = snapshotEditable(el);
    event?.preventDefault();
    // Shift+F10 arrives without an event and wants menu focus.
    setEditMenuRetainsFocus(event?.button === 2);
    setEditMenuAnchor(event ? { x: event.clientX, y: event.clientY } : el);
    return true;
  };

  const clearSecretSelection = () => { secretSelection = null; };
  return {
    editMenuAnchor,
    setEditMenuAnchor,
    editMenuRetainsFocus,
    markSecretTarget,
    setMarkSecretTarget,
    editContextMenuItems,
    openEditContextMenu,
    clearSecretSelection,
  };
}
