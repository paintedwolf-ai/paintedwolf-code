import type { IconSlot } from "../../contributions/theme-vocabulary.generated.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import type { FileBufferKind } from "../documents/project-files-buffer-kind.ts";

export type FileTabIdentity = { kind: FileBufferKind; chatContent?: Pick<ChatContentDocument, "kind"> };

export type FileTabKind = {
  icon: IconSlot;
  label: string;
};

/** File comparisons retain file identity; these views have no file in the tree. */
const SPECIAL_TAB_KINDS = {
  walk: { icon: "file-group", label: "Group summary" },
  diffs: { icon: "diff", label: "All diffs" },
  chat: { icon: "session", label: "Chat content" },
  trust: { icon: "shield", label: "Trust changes" },
} satisfies Partial<Record<FileBufferKind, FileTabKind>>;

const CHAT_TAB_KINDS = {
  tool: { icon: "tool", label: "Tool call details" },
  approval: { icon: "settings-approvals", label: "Approval details" },
} satisfies Record<ChatContentDocument["kind"], FileTabKind>;

export function specialTabKind(buffer: FileTabIdentity): FileTabKind | null {
  if (buffer.kind === "chat" && buffer.chatContent) return CHAT_TAB_KINDS[buffer.chatContent.kind];
  switch (buffer.kind) {
    case "walk":
    case "diffs":
    case "chat":
    case "trust":
      return SPECIAL_TAB_KINDS[buffer.kind];
    default:
      return null;
  }
}

export function fileTabKind(buffer: FileTabIdentity, previousVersion = false): FileTabKind | null {
  const special = specialTabKind(buffer);
  if (special) return special;
  if (buffer.kind === "diff") return { icon: "rewind", label: "File comparison" };
  if (previousVersion) return { icon: "rewind", label: "Previous version" };
  return null;
}
