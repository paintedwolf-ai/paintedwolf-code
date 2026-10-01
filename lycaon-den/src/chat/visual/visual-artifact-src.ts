import type { LycaonClient } from "../../api/client.ts";

const srcByArtifact = new Map<string, string>();
const pendingSrcByArtifact = new Map<string, Promise<string>>();
let cacheGeneration = 0;

/** Object URLs survive row virtualization and release at session teardown. */
export function visualArtifactSrc(
  client: LycaonClient,
  sessionId: string,
  artifactId: string,
): Promise<string> {
  const normalizedSessionId = sessionId.trim();
  const normalizedArtifactId = artifactId.trim();
  if (!normalizedSessionId || !normalizedArtifactId) {
    return Promise.reject(new Error("artifact source missing session or id"));
  }
  const key = `${normalizedSessionId}\0${normalizedArtifactId}`;
  const cached = srcByArtifact.get(key);
  if (cached) return Promise.resolve(cached);
  const pending = pendingSrcByArtifact.get(key);
  if (pending) return pending;

  const generation = cacheGeneration;
  const next = client
    .getSessionArtifact(normalizedSessionId, normalizedArtifactId)
    .then((blob) => {
      const src = URL.createObjectURL(blob);
      if (generation === cacheGeneration && pendingSrcByArtifact.get(key) === next) {
        srcByArtifact.set(key, src);
      } else {
        // Late responses belong to a retired cache generation.
        URL.revokeObjectURL(src);
      }
      return src;
    })
    .finally(() => {
      if (pendingSrcByArtifact.get(key) === next) {
        pendingSrcByArtifact.delete(key);
      }
    });
  pendingSrcByArtifact.set(key, next);
  return next;
}

/** Cached object URL for an already-fetched artifact — remounts read it
 * synchronously so the img mounts in the card's first frame. */
export function visualArtifactSrcSync(
  sessionId: string,
  artifactId: string,
): string | undefined {
  return srcByArtifact.get(`${sessionId.trim()}\0${artifactId.trim()}`);
}

/** Deletion retires every session's copy, including in-flight cache fills. */
export function invalidateVisualArtifactSrc(artifactId: string): void {
  const suffix = `\0${artifactId.trim()}`;
  for (const [key, src] of srcByArtifact) {
    if (!key.endsWith(suffix)) continue;
    URL.revokeObjectURL(src);
    srcByArtifact.delete(key);
  }
  for (const key of pendingSrcByArtifact.keys()) {
    if (key.endsWith(suffix)) pendingSrcByArtifact.delete(key);
  }
}

/** Release object URLs when their session-scoped transcript state is discarded. */
export function clearVisualArtifactSrcs(): void {
  cacheGeneration += 1;
  for (const src of srcByArtifact.values()) URL.revokeObjectURL(src);
  srcByArtifact.clear();
  pendingSrcByArtifact.clear();
}
