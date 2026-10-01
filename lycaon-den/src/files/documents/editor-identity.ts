import type { IconSlot } from "../../contributions/theme-vocabulary.generated.ts";
import type { FileDocumentOpening } from "./file-document-opening.ts";

type EditorIdentityId =
  | "editing"
  | "historical-version"
  | "worker-draft"
  | "read-only-file"
  | "preview"
  | "deleted"
  | "deleted-on-disk"
  | "opening"
  | "reconnecting"
  | "editing-error"
  | "editing-paused"
  | "large-file"
  | "view-only";

type EditorIdentity = {
  readonly id: EditorIdentityId;
  readonly label: string;
  readonly icon: IconSlot;
  readonly readOnly: boolean;
};

const IDENTITIES: Record<EditorIdentityId, EditorIdentity> = {
  editing: {
    id: "editing",
    label: "Editing",
    icon: "editor-editing",
    readOnly: false,
  },
  "historical-version": {
    id: "historical-version",
    label: "Historical version",
    icon: "editor-version",
    readOnly: true,
  },
  "worker-draft": {
    id: "worker-draft",
    label: "Worker draft",
    icon: "editor-worker-draft",
    readOnly: true,
  },
  "read-only-file": {
    id: "read-only-file",
    label: "Read-only file",
    icon: "editor-read-only",
    readOnly: true,
  },
  preview: {
    id: "preview",
    label: "Preview",
    icon: "editor-preview",
    readOnly: true,
  },
  deleted: {
    id: "deleted",
    label: "Deleted",
    icon: "editor-deleted",
    readOnly: true,
  },
  "deleted-on-disk": {
    id: "deleted-on-disk",
    label: "Deleted on disk",
    icon: "editor-deleted",
    readOnly: false,
  },
  "view-only": {
    id: "view-only",
    label: "View only",
    icon: "editor-view-only",
    readOnly: true,
  },
  opening: { id: "opening", label: "Opening file…", icon: "editor-editing", readOnly: true },
  reconnecting: { id: "reconnecting", label: "Reconnecting…", icon: "editor-editing", readOnly: true },
  "editing-error": { id: "editing-error", label: "Editing unavailable", icon: "editor-view-only", readOnly: true },
  "editing-paused": { id: "editing-paused", label: "Editing paused", icon: "editor-editing", readOnly: true },
  "large-file": { id: "large-file", label: "Large file preview", icon: "editor-preview", readOnly: true },
};

export function resolveEditorIdentity(args: {
  historical: boolean;
  workerDraft: boolean;
  deleted: boolean;
  /** The document holds a draft for a path whose file left the disk. */
  absentDraft?: boolean;
  writable: boolean | null;
  preview: boolean;
  editable: boolean;
  loading?: boolean;
  opening?: FileDocumentOpening;
  paused?: boolean;
  overLimit?: boolean;
  unsupported?: boolean;
  documentPending?: boolean;
}): EditorIdentity {
  if (args.historical) return IDENTITIES["historical-version"];
  if (args.workerDraft) return IDENTITIES["worker-draft"];
  if (args.absentDraft && args.editable) return IDENTITIES["deleted-on-disk"];
  if (args.deleted && !args.absentDraft) return IDENTITIES.deleted;
  if (args.writable === false) return IDENTITIES["read-only-file"];
  if (args.preview) return IDENTITIES.preview;
  if (args.overLimit) return IDENTITIES["large-file"];
  if (args.unsupported) return IDENTITIES["view-only"];
  if (args.editable) return IDENTITIES.editing;
  if (args.loading || args.opening?.status === "opening") return IDENTITIES.opening;
  if (args.opening?.status === "reconnecting") return IDENTITIES.reconnecting;
  if (args.paused) return IDENTITIES["editing-paused"];
  if (args.opening?.status !== "error" && args.documentPending) return IDENTITIES.opening;
  return IDENTITIES["editing-error"];
}

type EditorStateActionId =
  | "restore-version"
  | "restore-deleted"
  | "open-current"
  | "retry-editing"
  | "make-editable";

type EditorStateAction = {
  readonly id: EditorStateActionId;
  readonly label: string;
  readonly tip: string;
  readonly icon: IconSlot;
};

export function editorStateAction(
  identity: EditorIdentity,
): EditorStateAction | null {
  switch (identity.id) {
    case "historical-version":
      return {
        id: "restore-version",
        label: "Restore this version",
        tip: "Make this version the current working file",
        icon: "editor-restore",
      };
    case "worker-draft":
      return {
        id: "open-current",
        label: "Open current file",
        tip: "Open the current project file",
        icon: "editor-open-current",
      };
    case "deleted":
      return {
        id: "restore-deleted",
        label: "Restore file",
        tip: "Bring back the contents this deletion removed",
        icon: "editor-restore",
      };
    case "read-only-file":
      return {
        id: "make-editable",
        label: "Make editable",
        tip: "Add owner-write permission to this file",
        icon: "editor-unlock",
      };
    case "editing-error":
      return { id: "retry-editing", label: "Retry editing", tip: "Reconnect this file to its document without discarding edits", icon: "editor-editing" };
    case "editing":
    case "deleted-on-disk":
    case "preview":
    case "view-only":
    case "opening":
    case "reconnecting":
    case "editing-paused":
    case "large-file":
      return null;
  }
}
