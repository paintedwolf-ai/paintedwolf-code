import type { SecretSpanState } from "../../../api/types.ts";
import type { SecretScreenSummary, SecretSpanMark } from "./secret-span-model.ts";

export const SECRET_SPAN_COPY = {
  stateLabel: {
    tracked: "Managed secret",
    retired: "Retired secret",
    detected: "Possible credential",
  } satisfies Record<SecretSpanState, string>,

  stateHint: {
    tracked:
      "The agent never reads this value. It uses the reference, and only in chats this secret is available to.",
    retired:
      "This value was replaced or its secret was revoked, so its reference no longer resolves. The bytes are still screened, and the file still holds a live credential.",
    detected:
      "Screened by shape rather than by exact provenance. Marking it protects the exact bytes.",
  } satisfies Record<SecretSpanState, string>,

  markSelection: "Mark as secret…",
  trackSpan: "Track as secret…",
  ignoreInProject: "Ignore this value in this project…",
  findOtherUses: "Find other uses in this file",
  copyReference: "Copy reference",
  openInSecrets: "Open in Secrets",

  loading: "Checking the selection…",
  sheetTitle: "Mark as secret",
  sheetLede: (path: string, line: number) => `${path} · line ${line}`,
  sheetFromDraft: "from the unsaved draft",
  sheetName: "Name",
  sheetNamePlaceholder: "What this credential is",
  sheetPurpose: "Purpose",
  sheetPurposePlaceholder: "What it is used for",
  sheetPurposeHint:
    "Shown to you in Secrets and to the agent beside the reference.",
  sheetScopeHint:
    "Project scope: a value in a file outlives any one chat, so every chat in this project may use the reference. The value lives in the encrypted credential vault.",
  sheetNoEditHint:
    "The file is not modified. Marking stores the value and screens these exact bytes everywhere the agent could carry them out.",
  sheetCapturing: "Capturing",
  sheetTrimmed: (count: number) =>
    `Trimmed ${count} surrounding character${count === 1 ? "" : "s"}`,
  sheetKeepTrim: "Undo trim",
  sheetRestoreTrim: "Trim again",
  sheetSubmit: "Mark as secret",
  sheetSubmitting: "Marking…",
  sheetCancel: "Cancel",
  sheetCaptureFailed: "This range could not be read. Try selecting it again.",
  sheetAlreadyProtected:
    "A managed secret already protects these characters, so there is nothing new to protect.",

  markedToast: (name: string) => `${name} is now a managed secret`,
  markFailed: "This value could not be marked.",
  staleDocument:
    "The file changed since that selection. Select the value again.",
  markLeftSecretActive:
    "The selection was not protected, and the secret it created is still active. Revoke it in this project's secrets settings.",
  markSecretMayBeActive:
    "The selection was not protected, and the app cannot tell whether a secret was created for it. Check this project's secrets settings before trying again.",
  unsyncedDraft:
    "Your latest edits have not reached the host, so it cannot read the bytes you selected. Try again in a moment.",

  chipNotScreened: "Not screened",
  chipTruncated: "Screened in part",
  chipNotScreenedTip:
    "Secret highlighting is unavailable for this buffer.",
  chipTruncatedTip: (bytes: number) =>
    `Secret highlighting covers the first ${formatBytes(bytes)} of this buffer.`,

  reportTitle: "Secret screen",
  reportNone: "No marks in the screened text.",
  reportNeverClean:
    "No pattern catalog recognizes every credential, so this is a count of what was found — never an assurance about what is absent.",
  reportCatalog: (version: string) => `Catalog ${version}`,
  reportNoCatalog: "No vendor catalog loaded",
  reportRevision: (revision: number) => `Screened at revision ${revision}`,
  reportCounts: (summary: SecretScreenSummary) => {
    const parts: string[] = [];
    if (summary.tracked > 0) parts.push(`${summary.tracked} managed`);
    if (summary.retired > 0) parts.push(`${summary.retired} retired`);
    if (summary.detected > 0) parts.push(`${summary.detected} untracked`);
    return parts.join(" · ");
  },
} as const;

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${Math.round(bytes / (1024 * 1024))} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} bytes`;
}

export function secretSpanFact(mark: SecretSpanMark): string {
  const rule = mark.ruleTitle.trim();
  const label = SECRET_SPAN_COPY.stateLabel[mark.state];
  return rule ? `${rule} · ${label.toLowerCase()}` : label;
}
