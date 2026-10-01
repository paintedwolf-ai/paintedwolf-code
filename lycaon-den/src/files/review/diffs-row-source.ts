import type { LycaonClient } from "../../api/client.ts";
import type {
  SourceComparisonDigest,
  SourceComparisonSelector,
  SourceGitReviewFile,
  SourceWalkEffect,
  SourceWalkFile,
} from "../../api/types.ts";
import { comparisonSelector, sourceReaderAccess, type SourceReaderAccess } from "../../api/source-reader.ts";
import type { DiffRowSource } from "../../components/source/diff/diff-row-source.ts";
import { sourceChangeFromPresence, type SourceReaderChange } from "../../components/source/reader/source-reader-change.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { contributorsLabel, effectContributors } from "./review-model.ts";
import { scopeComparisonTarget, scopeSessionId } from "../tree/scope-resolution.ts";
import type { DiffsAddress, GitDiffsAddress } from "./diffs-address.ts";
import type { DiffsFile } from "./diffs-inventory.ts";
import { digestIsBinary, digestIsNoop, digestStat } from "./diffs-digests.ts";

export type DiffsRowContext = {
  client: () => LycaonClient | null;
  projectId: () => string;
  address: () => DiffsAddress;
  markUserEdits: () => boolean;
  rootRefs: () => readonly ResolveProjectRoot[] | undefined;
};

export function diffsFileWrites(file: SourceWalkFile): readonly SourceWalkEffect[] {
  return [...file.effects].sort((a, b) => a.ordinal - b.ordinal);
}

export function diffsFileChange(file: SourceWalkFile): SourceReaderChange {
  const writes = diffsFileWrites(file);
  const first = writes[0];
  const last = writes[writes.length - 1];
  if (!first || !last) {
    // Infer change from commit op and tip presence when no writes are recorded.
    return file.commit
      ? sourceChangeFromPresence(file.commit.op !== "create", file.tip.state !== "absent")
      : "changed";
  }
  return sourceChangeFromPresence(first.op !== "create", last.op !== "delete");
}

function writeLabel(effect: SourceWalkEffect): string {
  return effect.tool_name?.trim() || effect.cause.replaceAll("_", " ") || effect.op;
}

export function diffsNetSelector(context: DiffsRowContext, file: SourceWalkFile): SourceComparisonSelector {
  const address = context.address();
  if (address.kind === "turn") {
    return {
      kind: "turn",
      file_id: file.file_id,
      session_id: address.sessionId,
      turn: address.turn,
      ...(context.markUserEdits() ? {} : { mark_user_edits: false }),
    };
  }
  return comparisonSelector(scopeComparisonTarget(context.projectId(), file.root_id, file.path));
}

export function diffsRowSource(context: DiffsRowContext, file: SourceWalkFile,
  digest: () => SourceComparisonDigest | undefined = () => undefined): DiffRowSource {
  const writes = diffsFileWrites(file);
  const change = diffsFileChange(file);
  const address = file.file_id || `${file.root_id}\u0000${file.path}`;
  const revisions = writes.map((effect, index) => ({
    key: effect.id,
    label: String(index + 1),
    ariaLabel: `Write ${index + 1} of ${writes.length}, ${writeLabel(effect)} by ${contributorsLabel(effectContributors(effect))}`,
  }));
  const selectorFor = (revision: number | null): SourceComparisonSelector | null => {
    if (revision === null) return diffsNetSelector(context, file);
    const effect = writes[revision];
    return effect ? { kind: "effect", effect_id: effect.id } : null;
  };
  // Each comparison reads the workspace of the chat it answered for.
  const sessionId = () => {
    const held = context.address();
    return held.kind === "turn" ? held.sessionId : scopeSessionId(context.projectId());
  };
  const opened = new Map<string, { client: LycaonClient; access: SourceReaderAccess }>();
  return {
    get key() {
      const held = context.address();
      const scope = held.kind === "turn" ? `${held.sessionId}:${held.turn}` : "lens";
      return `diffs:${scope}:${address}`;
    },
    get projectId() { return context.projectId(); },
    get sessionId() { return sessionId() ?? null; },
    get rootRefs() { return context.rootRefs(); },
    get chatDestination() {
      const held = context.address();
      return held.kind === "turn"
        ? { projectId: context.projectId(), sessionId: held.sessionId }
        : null;
    },
    get revisions() { return revisions; },
    get netLabel() { return "Net"; },
    get revisionsLabel() { return `${writes.length} writes to ${file.path} in this comparison`; },
    get noopNote() {
      const d = digest();
      if (file.commit?.availability === "binary" || digestIsBinary(d)) {
        return "This file is binary and cannot be shown as a text diff.";
      }
      return "This comparison leaves the file as it found it.";
    },
    path: (revision) => (revision === null ? file.path : writes[revision]?.path ?? file.path),
    rootId: (revision) => (revision === null ? file.root_id : writes[revision]?.root_id ?? file.root_id),
    openable: (revision) => (file.tip.state === "absent" || (revision === null && change === "deleted") ? false : undefined),
    change: (revision) => {
      const effect = revision === null ? null : writes[revision];
      return effect
        ? sourceChangeFromPresence(effect.op !== "create", effect.op !== "delete")
        : change;
    },
    stat: (revision) => (revision === null ? digestStat(digest()) : null),
    isNoop: (revision) => revision === null && digestIsNoop(digest()),
    isBinary: (revision) => {
      if (revision !== null) return false;
      const d = digest();
      return file.commit?.availability === "binary" || digestIsBinary(d);
    },
    access: (revision) => {
      const client = context.client();
      const selector = selectorFor(revision);
      if (!client || !selector) return undefined;
      const projectId = context.projectId(), session = sessionId();
      const key = JSON.stringify([projectId, session ?? null, selector]);
      const held = opened.get(key);
      if (held?.client === client) return held.access;
      const access = sourceReaderAccess(client, projectId, selector, session);
      opened.set(key, { client, access });
      return access;
    },
  };
}

export function gitDiffsNetSelector(address: GitDiffsAddress, file: SourceGitReviewFile): SourceComparisonSelector {
  return {
    kind: "git_range",
    root_id: address.rootId,
    before_commit: address.beforeCommit,
    after_commit: address.afterCommit,
    path: file.path,
  };
}

export function gitDiffsRowSource(context: DiffsRowContext, address: GitDiffsAddress, file: SourceGitReviewFile,
  digest: () => SourceComparisonDigest | undefined = () => undefined): DiffRowSource {
  const change = sourceChangeFromPresence(file.op !== "create", file.op !== "delete");
  const selector = gitDiffsNetSelector(address, file);
  let opened: { client: LycaonClient; projectId: string; access: SourceReaderAccess } | undefined;
  return {
    key: `diffs:git:${address.rootId}:${address.beforeCommit}:${address.afterCommit}:${file.path}`,
    get projectId() { return context.projectId(); },
    sessionId: null,
    get rootRefs() { return context.rootRefs(); },
    chatDestination: null,
    revisions: [],
    netLabel: "Net",
    revisionsLabel: `${file.path} in this comparison`,
    get noopNote() {
      if (file.binary || digestIsBinary(digest())) {
        return "This file is binary and cannot be shown as a text diff.";
      }
      return "This comparison leaves the file as it found it.";
    },
    path: () => file.path,
    rootId: () => address.rootId,
    openable: () => (change === "deleted" ? false : undefined),
    change: () => change,
    stat: (revision) => (revision === null ? (digestStat(digest()) ?? { added: file.insertions, removed: file.deletions }) : null),
    isNoop: (revision) => revision === null && digestIsNoop(digest()),
    isBinary: (revision) => {
      if (revision !== null) return false;
      return file.binary || digestIsBinary(digest());
    },
    access: (revision) => {
      const client = context.client();
      if (!client || revision !== null) return undefined;
      const projectId = context.projectId();
      if (opened?.client !== client || opened.projectId !== projectId) {
        opened = { client, projectId, access: sourceReaderAccess(client, projectId, selector, undefined) };
      }
      return opened.access;
    },
  };
}

export function diffsFileNetSelector(context: DiffsRowContext, row: DiffsFile): SourceComparisonSelector {
  return row.kind === "walk" ? diffsNetSelector(context, row.file) : gitDiffsNetSelector(row.address, row.file);
}

export function diffsFileRowSource(context: DiffsRowContext, row: DiffsFile,
  digest: () => SourceComparisonDigest | undefined = () => undefined): DiffRowSource {
  return row.kind === "walk"
    ? diffsRowSource(context, row.file, digest)
    : gitDiffsRowSource(context, row.address, row.file, digest);
}
