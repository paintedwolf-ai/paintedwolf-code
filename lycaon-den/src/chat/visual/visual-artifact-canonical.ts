import type { Message, VisualArtifactSource } from "../../api/types.ts";
import { isDenTranscriptMessage } from "../transcript/projection/message-transcript.ts";

/** Transcript projection face. */
export type ArtifactFace = "producer" | "present";

/** Mime, caption, source, and pixel size from a tool_result.visual that named this id. */
export type VisualArtifactMeta = {
  mime: string;
  caption: string;
  source: VisualArtifactSource;
  width?: number;
  height?: number;
};

/** First full-render site for one artifact id on one face. */
export type CanonicalArtifactPlacement = {
  artifactId: string;
  rowKey: string;
  /** `visual:{id}` or `{row}:present:{id}`. */
  entryKey: string;
  kind: ArtifactFace;
  caption: string;
  source: VisualArtifactSource;
  mime: string;
};

/** Face-scoped canonical sites plus metadata for present stubs. */
export type CanonicalArtifactPlacements = {
  site(
    kind: ArtifactFace,
    artifactId: string,
  ): CanonicalArtifactPlacement | undefined;
  meta(artifactId: string): VisualArtifactMeta | undefined;
  values(): readonly CanonicalArtifactPlacement[];
};

export function visualProducerEntryKey(artifactId: string): string {
  return `visual:${artifactId.trim()}`;
}

export function visualPresentEntryKey(rowKey: string, artifactId: string): string {
  return `${rowKey.trim()}:present:${artifactId.trim()}`;
}

export function faceSiteKey(kind: ArtifactFace, artifactId: string): string {
  return `${kind}:${artifactId.trim()}`;
}

/** Face this entry key belongs to, or null when it matches neither constructor. */
export function artifactFaceFromEntryKey(
  entryKey: string,
  artifactId: string,
): ArtifactFace | null {
  const key = entryKey.trim();
  const id = artifactId.trim();
  if (!key || !id) return null;
  if (key === visualProducerEntryKey(id)) return "producer";
  if (key.endsWith(`:present:${id}`)) return "present";
  return null;
}

function sortMessagesByOrd(messages: readonly Message[]): Message[] {
  return [...messages].sort((a, b) => {
    const ord = (a.ord ?? 0) - (b.ord ?? 0);
    if (ord !== 0) return ord;
    return a.id.localeCompare(b.id);
  });
}

function recordVisualMeta(
  metas: Map<string, VisualArtifactMeta>,
  visual: {
    id?: string;
    mime?: string;
    caption?: string;
    source?: VisualArtifactSource;
    width?: number;
    height?: number;
  },
): void {
  const id = visual.id?.trim() ?? "";
  if (!id || metas.has(id)) return;
  metas.set(id, {
    mime: visual.mime?.trim() ?? "",
    caption: visual.caption?.trim() ?? "",
    source: visual.source ?? "render",
    width: visual.width,
    height: visual.height,
  });
}

export type CanonicalArtifactPlacementOptions = {
  /** Worker rows contribute metadata without claiming transcript placement. */
  metaMessages?: readonly Message[];
};

/** Message order selects the first full rendering on each transcript face. */
export function buildCanonicalArtifactPlacement(
  messages: readonly Message[],
  options?: CanonicalArtifactPlacementOptions,
): CanonicalArtifactPlacements {
  const sites = new Map<string, CanonicalArtifactPlacement>();
  const metas = new Map<string, VisualArtifactMeta>();

  const claim = (placement: CanonicalArtifactPlacement): void => {
    const id = placement.artifactId.trim();
    if (!id) return;
    const key = faceSiteKey(placement.kind, id);
    if (sites.has(key)) return;
    sites.set(key, { ...placement, artifactId: id });
  };

  for (const msg of messages) {
    const visual = msg.tool_result?.visual;
    if (visual) recordVisualMeta(metas, visual);
  }
  for (const msg of options?.metaMessages ?? []) {
    const visual = msg.tool_result?.visual;
    if (visual) recordVisualMeta(metas, visual);
  }

  for (const msg of sortMessagesByOrd(messages)) {
    const visual = msg.tool_result?.visual;
    if (!isDenTranscriptMessage(msg)) continue;
    if (visual?.id?.trim()) {
      const id = visual.id.trim();
      const meta = metas.get(id);
      claim({
        artifactId: id,
        rowKey: msg.id,
        entryKey: visualProducerEntryKey(id),
        kind: "producer",
        caption: meta?.caption ?? visual.caption?.trim() ?? "",
        source: meta?.source ?? visual.source,
        mime: meta?.mime ?? visual.mime?.trim() ?? "",
      });
    }
    if (msg.role !== "user" && msg.role !== "assistant") continue;
    for (const raw of msg.artifact_ids ?? []) {
      const id = raw.trim();
      if (!id) continue;
      const meta = metas.get(id);
      claim({
        artifactId: id,
        rowKey: msg.id,
        entryKey: visualPresentEntryKey(msg.id, id),
        kind: "present",
        caption: meta?.caption ?? "",
        source: meta?.source ?? "render",
        mime: meta?.mime ?? "",
      });
    }
  }

  return {
    site(kind, artifactId) {
      return sites.get(faceSiteKey(kind, artifactId.trim()));
    },
    meta(artifactId) {
      return metas.get(artifactId.trim());
    },
    values() {
      return [...sites.values()];
    },
  };
}

/** Unknown or empty keys render full. */
export function isCanonicalArtifactEntry(
  placements: CanonicalArtifactPlacements,
  artifactId: string,
  entryKey: string,
): boolean {
  const id = artifactId.trim();
  const key = entryKey.trim();
  if (!id || !key) return true;
  const kind = artifactFaceFromEntryKey(key, id);
  if (!kind) return true;
  const site = placements.site(kind, id);
  if (!site) return true;
  return site.entryKey === key;
}
