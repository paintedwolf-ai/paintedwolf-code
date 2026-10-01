import type {
  CitationGrounding,
  NavigationReference,
} from "../../api/types.ts";
import { escapeHtml } from "./html-escape.ts";
import { renderProjectPathButton } from "./markdown-project-path-link.ts";
import {
  parseProsePathCandidate,
  type ProsePathTarget,
} from "./prose-path-parse.ts";

export type ProseCitationIndex = {
  /** Identical citation sets share a cache key. */
  key: string;
  /** Normalized repository paths mapped to the first cited line. */
  paths: Map<string, number | undefined>;
  /** Host-marked non-openable cited paths. */
  blocked: ReadonlySet<string>;
};

export type ProseNavigationIndex = {
  /** Identical reference sets share a cache key. */
  key: string;
  /** Mention lookup for the current occurrence. */
  mentions: Map<string, NavigationReference>;
  references: Map<string, NavigationReference>;
  occurrences: Map<string, NavigationReference[]>;
  positions: Map<string, number>;
};

export type ProsePathResolveOpts = {
  index?: ProseCitationIndex;
  navigation?: ProseNavigationIndex;
};


function normalizeCitedPath(raw: string | undefined): string {
  let path = (raw ?? "").trim().replace(/\\/g, "/");
  while (path.startsWith("./")) path = path.slice(2);
  return path;
}

type CitedRow = { path?: string; line?: number; openable?: boolean };

export function buildProseCitationIndex(
  grounding: CitationGrounding | null | undefined,
): ProseCitationIndex | undefined {
  if (!grounding) return undefined;
  // Cited lines outrank incidental reads of the same path.
  const rows: CitedRow[] = [
    ...(grounding.cited_evidence ?? []),
    ...(grounding.findings ?? []),
    ...(grounding.evidence_records ?? []).filter(
      (rec) => rec.shape === "file_region",
    ),
  ];
  const paths = new Map<string, number | undefined>();
  const blocked = new Set<string>();
  for (const row of rows) {
    const path = normalizeCitedPath(row.path);
    if (!path) continue;
    if (row.openable === false) {
      blocked.add(path);
      continue;
    }
    const line =
      typeof row.line === "number" && Number.isFinite(row.line) && row.line > 0
        ? row.line
        : undefined;
    if (!paths.has(path)) {
      paths.set(path, line);
    } else if (paths.get(path) === undefined && line !== undefined) {
      paths.set(path, line);
    }
  }
  if (paths.size === 0 && blocked.size === 0) return undefined;
  const key = [
    ...[...paths.keys()]
      .sort()
      .map((p) => `${p}\u0001${paths.get(p) ?? ""}`),
    ...[...blocked].sort().map((p) => `!${p}`),
  ].join("\u0002");
  return { key, paths, blocked };
}

export function buildProseNavigationIndex(
  refs: readonly NavigationReference[] | null | undefined,
): ProseNavigationIndex | undefined {
  if (!refs || refs.length === 0) return undefined;
  const mentions = new Map<string, NavigationReference>();
  const references = new Map<string, NavigationReference>();
  const occurrences = new Map<string, NavigationReference[]>();
  for (const ref of refs) {
    if (!ref.mention || !ref.path || !ref.project_id || !ref.status) continue;
    mentions.set(ref.mention, ref);
    references.set(ref.id, ref);
    const key = JSON.stringify([ref.syntax, ref.mention]);
    occurrences.set(key, [...(occurrences.get(key) ?? []), ref]);
  }
  if (references.size === 0) return undefined;
  return { key: JSON.stringify(refs), mentions, references, occurrences, positions: new Map() };

}

export function navigationOccurrence(candidate: string, syntax: string, index?: ProseNavigationIndex): ProseNavigationIndex | undefined {
  if (!index) return undefined;
  const key = JSON.stringify([syntax, candidate]);
  const position = index.positions.get(key) ?? 0;
  index.positions.set(key, position + 1);
  const ref = index.occurrences.get(key)?.[position];
  return { ...index, mentions: new Map(ref ? [[candidate, ref]] : []) };
}

export function renderNavigationRequest(
  candidate: string,
  labelHtml: string,
  navigation?: ProseNavigationIndex,
): string | undefined {
  const ref = navigation?.mentions.get(candidate);
  if (!ref || ref.status === "resolved") return undefined;
  if (ref.status !== "ambiguous" && !ref.explicit) return undefined;
  const tip = ref.status === "ambiguous" ? "Choose a referenced file" : "Open file — check availability";
  const choice = ref.status === "ambiguous" ? " den-source-path-choice" : "";
  return `<button type="button" class="den-source-path-link${choice}"${ref.status === "ambiguous" ? ' aria-haspopup="dialog"' : ""} data-den-navigation-reference="${escapeHtml(ref.id)}" data-tip="${escapeHtml(tip)}">${labelHtml}</button>`;
}

/** Citation matching requires the complete path. */
export function matchProseCitation(
  index: ProseCitationIndex,
  candidate: string,
): ProsePathTarget | undefined {
  const parsed = parseProsePathCandidate(candidate);
  if (!parsed) return undefined;
  const { path, line, endLine } = parsed;

  if (index.paths.has(path)) {
    return { path, line: line ?? index.paths.get(path), endLine };
  }

  return undefined;
}

export function matchProseNavigation(
  index: ProseNavigationIndex,
  candidate: string,
): ProsePathTarget | undefined {
  const parsed = parseProsePathCandidate(candidate);
  const ref = index.mentions.get(candidate);
  if (!ref || ref.status !== "resolved") return undefined;
  return {
    reference: ref.id,
    jobId: ref.worker_id,
    projectId: ref.project_id,
    rootId: ref.root_id,
    path: ref.path,
    entryKind: ref.entry_kind,
    line: ref.line ?? parsed?.line,
    endLine: ref.end_line ?? parsed?.endLine,
  };
}

/** Host navigation resolves parsed mentions; citations supply exact fallback destinations. */
export function resolveProsePathTarget(
  candidate: string,
  opts?: ProsePathResolveOpts,
): ProsePathTarget | undefined {
  if (opts?.navigation?.mentions.has(candidate)) {
    const target = matchProseNavigation(opts.navigation, candidate);
    if (target && target.line == null) target.line = opts.index?.paths.get(target.path);
    return target;
  }
  const index = opts?.index;
  if (index) {
    const cited = matchProseCitation(index, candidate);
    if (cited) return cited;
  }
  return undefined;
}

function tryResolveTrimmed(
  token: string,
  opts?: ProsePathResolveOpts,
): { trimmed: string; target: ProsePathTarget } | undefined {
  // Trailing sentence punctuation is separate from the path.
  let trimmed = token;
  while (trimmed.endsWith(".")) {
    trimmed = trimmed.slice(0, -1);
  }
  if (!trimmed) return undefined;
  const target = resolveProsePathTarget(trimmed, opts);
  if (!target) return undefined;
  return { trimmed, target };
}

export function renderProsePathCodespan(
  text: string,
  projectId: string,
  opts?: ProsePathResolveOpts,
): string | undefined {
  opts = { ...opts, navigation: navigationOccurrence(text, "code", opts?.navigation) };
  const request = renderNavigationRequest(text, `<code>${escapeHtml(text)}</code>`, opts?.navigation);
  if (request) return request;
  const target = resolveProsePathTarget(text, opts);
  if (!target) return undefined;
  return renderProjectPathButton({
    reference: target.reference,
    jobId: target.jobId,
    projectId: target.projectId ?? projectId,
    rootId: target.rootId,
    path: target.path,
    entryKind: target.entryKind ?? "file",
    line: target.line,
    endLine: target.endLine,
    labelHtml: `<code>${escapeHtml(text)}</code>`,
    labelText: text,
  });
}

/** Only standalone, single-line fences can become path links. */
export function renderProsePathBlock(
  text: string,
  projectId: string,
  opts?: ProsePathResolveOpts,
): string | undefined {
  const candidate = text.trim();
  if (!candidate || candidate.includes("\n") || candidate.includes("\r")) {
    return undefined;
  }
  opts = { ...opts, navigation: navigationOccurrence(candidate, "fence", opts?.navigation) };
  const request = renderNavigationRequest(candidate, escapeHtml(candidate), opts?.navigation);
  if (request) return `<pre class="den-source-path-block"><code>${request}</code></pre>\n`;
  const target = resolveProsePathTarget(candidate, opts);
  if (!target) return undefined;
  const button = renderProjectPathButton({
    reference: target.reference,
    jobId: target.jobId,
    projectId: target.projectId ?? projectId,
    rootId: target.rootId,
    path: target.path,
    entryKind: target.entryKind ?? "file",
    line: target.line,
    endLine: target.endLine,
    labelHtml: escapeHtml(candidate),
    labelText: candidate,
  });
  return `<pre class="den-source-path-block"><code>${button}</code></pre>\n`;
}

// Maximal runs of path characters with an optional :line(-range) or #Lline tail.
const TEXT_TOKEN_RE = /[-\p{L}\p{N}_@./\\]+(?::\d+(?:-\d+)?)?(?:#L?\d+(?:-L?\d+)?)?/gu;

export function linkifyProsePathsInText(
  text: string,
  projectId: string,
  opts?: ProsePathResolveOpts,
): string | undefined {
  TEXT_TOKEN_RE.lastIndex = 0;
  let out = "";
  let last = 0;
  let matched = false;
  for (let m = TEXT_TOKEN_RE.exec(text); m; m = TEXT_TOKEN_RE.exec(text)) {
    const token = m[0];
    if (!token.includes(".") && !token.includes("/")) continue;
    const trimmed = token.replace(/\.+$/, "");
    const occurrence = { ...opts, navigation: navigationOccurrence(trimmed, "text", opts?.navigation) };
    const ref = occurrence.navigation?.mentions.get(trimmed);
    const request = ref && (ref.explicit || (ref.candidates?.length ?? 0) > 0)
      ? renderNavigationRequest(trimmed, escapeHtml(trimmed), occurrence.navigation) : undefined;
    if (request) {
      matched = true;
      out += escapeHtml(text.slice(last, m.index)) + request;
      last = m.index + trimmed.length;
      continue;
    }
    const hit = tryResolveTrimmed(token, occurrence);
    if (!hit) continue;
    matched = true;
    out += escapeHtml(text.slice(last, m.index));
    out += renderProjectPathButton({
      reference: hit.target.reference,
      jobId: hit.target.jobId,
      projectId: hit.target.projectId ?? projectId,
      rootId: hit.target.rootId,
      path: hit.target.path,
      entryKind: hit.target.entryKind ?? "file",
      line: hit.target.line,
      endLine: hit.target.endLine,
      labelHtml: escapeHtml(hit.trimmed),
      labelText: hit.trimmed,
    });
    last = m.index + hit.trimmed.length;
  }
  if (!matched) return undefined;
  out += escapeHtml(text.slice(last));
  return out;
}
