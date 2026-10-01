import type {
  ArtifactListItem,
  ArtifactReferenceCount,
  ArtifactReferenceKind,
} from "../../api/types.ts";

/** What each kind of durable claim means to someone deciding whether to delete. */
const KIND_COPY: Record<
  ArtifactReferenceKind,
  { one: string; many: (n: number) => string }
> = {
  project_cover: {
    one: "the project cover",
    many: () => "the project cover",
  },
  message_present: {
    one: "1 message that shows it",
    many: (n) => `${n} messages that show it`,
  },
  message_attachment: {
    one: "1 message it is attached to",
    many: (n) => `${n} messages it is attached to`,
  },
  tool_result: {
    one: "1 tool result",
    many: (n) => `${n} tool results`,
  },
};

/** Display order: the most consequential claim first. */
const KIND_ORDER: ArtifactReferenceKind[] = [
  "project_cover",
  "message_present",
  "message_attachment",
  "tool_result",
];

function normalize(
  references: readonly ArtifactReferenceCount[] | undefined,
): ArtifactReferenceCount[] {
  if (!references?.length) return [];
  const totals = new Map<ArtifactReferenceKind, number>();
  for (const ref of references) {
    const count = Math.max(0, Math.trunc(ref.count ?? 0));
    if (!count || !(ref.kind in KIND_COPY)) continue;
    totals.set(ref.kind, (totals.get(ref.kind) ?? 0) + count);
  }
  return KIND_ORDER.filter((kind) => totals.has(kind)).map((kind) => ({
    kind,
    count: totals.get(kind) as number,
  }));
}

/** One sentence naming what points at this artifact; empty when nothing does. */
export function referenceImpactSentence(item: ArtifactListItem): string {
  const parts = normalize(item.references).map((ref) =>
    ref.count === 1 ? KIND_COPY[ref.kind].one : KIND_COPY[ref.kind].many(ref.count),
  );
  if (parts.length === 0) return "";
  if (parts.length === 1) return `Used by ${parts[0]}.`;
  const last = parts[parts.length - 1];
  return `Used by ${parts.slice(0, -1).join(", ")} and ${last}.`;
}

/** Deletion preserves references already recorded in messages and tool results. */
export function deleteConfirmBody(item: ArtifactListItem): string {
  const impact = referenceImpactSentence(item);
  const tail =
    "The image is removed for good. Anywhere it was already shown keeps its reference and will read as deleted.";
  return impact ? `${impact} ${tail}` : tail;
}
