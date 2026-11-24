import type { HomeStart, SessionMode } from "../../api/types.ts";

/** Bytes a chip needs before the first prompt can carry it. */
export type ChipSnapshot =
  /** Bytes are being copied into the outbox. */
  | "pending"
  /** Bytes are durable in the outbox, or the chip needs none. */
  | "ready"
  /** The outbox lost or never received the bytes; the file must be chosen again. */
  | "missing"
  /** The source file changed after it was attached; the person decides. */
  | "changed";

type ChipBase = {
  /** Stable for the life of the launch; names the outbox blob. */
  itemId: string;
  name: string;
};

/** One Home attachment as the launch carries it. */
export type LaunchChip =
  | (ChipBase & {
      kind: "bytes";
      mime: string;
      byteLength: number;
      snapshot: ChipSnapshot;
    })
  | (ChipBase & {
      kind: "path";
      absolutePath: string;
      /** Extension-guessed; the host decides the real kind at import. */
      expected: "image" | "video" | "document";
      /** Size and modification time recorded at intake, when the platform reports them. */
      fingerprint?: PathFingerprint;
      snapshot: ChipSnapshot;
    })
  | (ChipBase & { kind: "reject"; reason: string });

export type PathFingerprint = { size: number; modifiedMs: number };

/** The host a record was composed for or first reached. */
export type HostBinding = { storeId: string; personId: string };

/** The editable Home composition; never sent until a person submits it. */
export type CompositionRecord = {
  id: string;
  revision: number;
  text: string;
  mode?: SessionMode;
  chips: LaunchChip[];
  updatedAt: number;
};

/** What the person asked for and has not discarded. */
export type LaunchIntent = "send" | "stop" | "discard";

/** The prompt exactly as it will be sent, captured before the first send. */
export type FirstPromptRequest = {
  text: string;
  attachmentLabels: string[];
  attachments: { blob_id: string; kind: string; name: string; mime?: string }[];
  references: Record<string, unknown>[];
};

/** One submitted Home idea and its progress toward a durable first turn. */
export type LaunchRecord = {
  launchId: string;
  /** Bumps with every local change; a stale write never replaces a newer one. */
  localRevision: number;
  createdAt: number;
  compositionId: string;
  compositionRevision: number;
  text: string;
  mode: SessionMode;
  chips: LaunchChip[];
  intent: LaunchIntent;
  /** The host this profile last reached when the idea was submitted. */
  expectedHost?: HostBinding;
  /** The host the launch was sent to; replay goes nowhere else. */
  host?: HostBinding;
  /** The person confirmed sending to a host other than the expected one. */
  destinationAccepted?: boolean;
  /** Latest host view, applied only in increasing revision order. */
  hostState?: HomeStart;
  /** The first-prompt operation; starts as the launch id. */
  promptOperationId: string;
  /** A send of promptOperationId may have reached the host. */
  sendAttempted?: boolean;
  firstPrompt?: FirstPromptRequest;
  /** Text proposed while the first send's outcome was unknown. */
  proposedText?: string;
};

export type HomeOutboxManifest = {
  format: 1;
  revision: number;
  /** The host this profile reached most recently. */
  knownHost?: HostBinding;
  composition?: CompositionRecord;
  launches: LaunchRecord[];
  /** The launch last shown in the foreground. */
  foregroundLaunchId?: string;
};

export function emptyManifest(): HomeOutboxManifest {
  return { format: 1, revision: 0, launches: [] };
}

export function sameHost(a: HostBinding | undefined, b: HostBinding | undefined): boolean {
  return Boolean(a && b && a.storeId === b.storeId && a.personId === b.personId);
}

/** Byte-backed chips whose bytes the outbox holds or still needs. */
export function chipItemIds(chips: readonly LaunchChip[]): string[] {
  return chips.filter((chip) => chip.kind === "bytes").map((chip) => chip.itemId);
}

/** Item ids every retained record still references. */
export function reachableItemIds(manifest: HomeOutboxManifest): Set<string> {
  const ids = new Set<string>();
  for (const chip of manifest.composition?.chips ?? []) if (chip.kind === "bytes") ids.add(chip.itemId);
  for (const launch of manifest.launches) for (const id of chipItemIds(launch.chips)) ids.add(id);
  return ids;
}

/** Applies a host view unless a newer one is already recorded. */
export function withHostState(record: LaunchRecord, next: HomeStart): LaunchRecord {
  const current = record.hostState;
  if (current && current.revision > next.revision) return record;
  if (current && current.revision === next.revision) return record;
  return { ...record, localRevision: record.localRevision + 1, hostState: next };
}

/** The chat the host bound to this launch, once it exists. */
export function boundChat(record: LaunchRecord): { projectId: string; sessionId: string } | null {
  const state = record.hostState;
  if (!state?.project_id || !state.session_id) return null;
  return { projectId: state.project_id, sessionId: state.session_id };
}

export function firstPromptAdmitted(record: LaunchRecord): boolean {
  const state = record.hostState;
  return Boolean(state?.first_prompt.admitted && state.first_prompt.operation_id === record.promptOperationId);
}

export function chipsNeedingAction(chips: readonly LaunchChip[]): LaunchChip[] {
  return chips.filter((chip) =>
    chip.kind === "reject" || chip.snapshot === "missing" || chip.snapshot === "changed");
}
