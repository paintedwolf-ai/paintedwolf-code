import type { Project } from "./types.ts";
import {
  isAbsolutePath,
  normalizePathForJail,
  pathIsUnderProjectRoots,
} from "../platform/files/reveal-in-file-manager.ts";
import { tauriPlatform, type TauriPlatform } from "../platform/runtime.ts";

/** Both path separators are accepted in labels. */
export function basenameOfPath(path: string): string {
  const parts = path.replace(/\\/g, "/").split("/").filter(Boolean);
  return parts[parts.length - 1] || path || "file";
}

export type ResolveProjectRoot = {
  id: string;
  path: string;
  is_primary?: boolean;
  /** Root token in `@label/relative-path` addresses. */
  label?: string;
};

/** Splits the host-qualified `@label/rel` path grammar. */
export function splitQualifiedRootPath(
  rel: string,
): { label: string; rest: string } | null {
  if (!rel.startsWith("@")) return null;
  const slash = rel.indexOf("/");
  const label = (slash < 0 ? rel : rel.slice(0, slash)).slice(1).trim();
  if (!label) return null;
  return { label, rest: slash < 0 ? "" : rel.slice(slash + 1) };
}

export type ResolveProjectFileInput = {
  roots: readonly ResolveProjectRoot[];
};

export type ResolveProjectFileOk = {
  absolutePath: string;
  rootId: string;
};

export type ResolveProjectFileErr = {
  error: string;
};

export type ResolveProjectFileResult =
  | ResolveProjectFileOk
  | ResolveProjectFileErr;

export function projectMatchesDir(
  project: Project,
  projectDir: string,
): boolean {
  const normalized = projectDir.replace(/\/$/, "");
  return project.roots.some(
    (r) => r.path.replace(/\/$/, "") === normalized,
  );
}

export function normalizeRepoRelativePath(relPath: string): string {
  return relPath.trim().replace(/\\/g, "/").replace(/^\/+/, "");
}

function joinRootRelative(
  rootAbs: string,
  relNormalized: string,
  platform: TauriPlatform | null,
): string {
  const rootN = normalizePathForJail(rootAbs, platform);
  const windows = platform === "windows";
  const sep = windows ? "\\" : "/";
  const relOs = windows
    ? relNormalized.replace(/\//g, "\\")
    : relNormalized;
  const base = rootN.endsWith(sep) ? rootN.slice(0, -1) : rootN;
  if (!relOs) return normalizePathForJail(base || rootN, platform);
  return normalizePathForJail(`${base}${sep}${relOs}`, platform);
}

function rootPrefixLength(
  absolutePath: string,
  rootAbs: string,
  platform: TauriPlatform | null,
): number {
  if (!pathIsUnderProjectRoots(absolutePath, [rootAbs], platform)) return -1;
  return normalizePathForJail(rootAbs, platform).length;
}

/** Nested roots resolve to their most specific container. */
export function longestMatchingRoot(
  absolutePath: string,
  roots: readonly ResolveProjectRoot[],
  platform: TauriPlatform | null = tauriPlatform(),
): ResolveProjectRoot | undefined {
  let best: ResolveProjectRoot | undefined;
  let bestLen = -1;
  for (const root of roots) {
    if (!root.path?.trim() || !root.id?.trim()) continue;
    const len = rootPrefixLength(absolutePath, root.path, platform);
    if (len > bestLen) {
      bestLen = len;
      best = root;
    }
  }
  return best;
}

/** Resolves paths contained by an attached root. */
export function resolveProjectFile(
  project: ResolveProjectFileInput,
  relPath: string,
  platform: TauriPlatform | null = tauriPlatform(),
): ResolveProjectFileResult {
  const roots = project.roots.filter(
    (r) => typeof r.id === "string" && r.id.trim() && typeof r.path === "string" && r.path.trim(),
  );
  if (roots.length === 0) {
    return { error: "no_roots" };
  }

  const raw = relPath.trim();
  if (!raw) {
    return { error: "empty_path" };
  }

  let candidate: string;
  if (isAbsolutePath(raw, platform)) {
    candidate = normalizePathForJail(raw, platform);
  } else {
    const rel = normalizeRepoRelativePath(raw);
    if (!rel) {
      return { error: "empty_path" };
    }
    const qualified = splitQualifiedRootPath(rel);
    if (qualified) {
      // An unknown root label cannot resolve beneath the primary root.
      const labeled = roots.find(
        (root) => root.label?.trim().toLowerCase() === qualified.label.toLowerCase(),
      );
      if (!labeled) {
        return { error: "outside_roots" };
      }
      candidate = qualified.rest
        ? joinRootRelative(labeled.path, qualified.rest, platform)
        : normalizePathForJail(labeled.path, platform);
    } else {
      const base = roots.find((root) => root.is_primary === true) ?? roots[0]!;
      candidate = joinRootRelative(base.path, rel, platform);
    }
  }

  if (!candidate || !isAbsolutePath(candidate, platform)) {
    return { error: "invalid_path" };
  }

  const containingRoot = longestMatchingRoot(candidate, roots, platform);
  if (!containingRoot) {
    return { error: "outside_roots" };
  }

  return {
    absolutePath: candidate,
    rootId: containingRoot.id,
  };
}

/** Relative paths use forward slashes; the root itself is ".". */
export function relativeUnderRoot(
  absolutePath: string,
  rootAbs: string,
  platform: TauriPlatform | null = tauriPlatform(),
): string | null {
  if (!pathIsUnderProjectRoots(absolutePath, [rootAbs], platform)) {
    return null;
  }
  const abs = normalizePathForJail(absolutePath, platform);
  const root = normalizePathForJail(rootAbs, platform);
  const windows = platform === "windows";
  const equal = windows
    ? abs.toLowerCase() === root.toLowerCase()
    : abs === root;
  if (equal) return ".";
  const sep = windows ? "\\" : "/";
  const prefix = root.endsWith(sep) ? root : `${root}${sep}`;
  return abs.slice(prefix.length).replace(/\\/g, "/");
}
