export type ThumbnailEntry = {
  dataUrl: string;
  capturedAt: number;
};

export type ThumbnailMap = Readonly<Record<string, ThumbnailEntry>>;

export const THUMBNAIL_CACHE_MAX = 24;

/** Minimum gap between captures of one project. */
export const THUMBNAIL_MIN_RECAPTURE_MS = 60_000;

/** True when the project needs a new capture. */
export function shouldRecaptureThumbnail(
  map: ThumbnailMap,
  projectId: string,
  now: number,
  minIntervalMs: number = THUMBNAIL_MIN_RECAPTURE_MS,
): boolean {
  const entry = map[projectId];
  if (!entry) return true;
  return now - entry.capturedAt >= minIntervalMs;
}

/** Insert/replace one thumbnail, evicting the oldest entries past the cap. */
export function putThumbnail(
  map: ThumbnailMap,
  projectId: string,
  dataUrl: string,
  now: number,
  max: number = THUMBNAIL_CACHE_MAX,
): ThumbnailMap {
  const next: Record<string, ThumbnailEntry> = {
    ...map,
    [projectId]: { dataUrl, capturedAt: now },
  };
  const ids = Object.keys(next);
  if (ids.length <= max) return next;
  ids
    .sort((a, b) => (next[a]!.capturedAt) - (next[b]!.capturedAt))
    .slice(0, ids.length - max)
    .forEach((id) => delete next[id]);
  return next;
}

export function dropThumbnail(map: ThumbnailMap, projectId: string): ThumbnailMap {
  if (!(projectId in map)) return map;
  const next = { ...map };
  delete next[projectId];
  return next;
}

export function clearThumbnails(map: ThumbnailMap): ThumbnailMap {
  return Object.keys(map).length === 0 ? map : {};
}

/** Parses valid persisted thumbnail entries. */
export function parseThumbnails(raw: string | null): ThumbnailMap {
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return {};
    const out: Record<string, ThumbnailEntry> = {};
    for (const [id, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (!value || typeof value !== "object") continue;
      const { dataUrl, capturedAt } = value as Partial<ThumbnailEntry>;
      if (typeof dataUrl === "string" && typeof capturedAt === "number") {
        out[id] = { dataUrl, capturedAt };
      }
    }
    return out;
  } catch {
    return {};
  }
}
