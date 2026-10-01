import type {
  FindingIgnoreEntry,
  FindingKind,
  FindingLedgerEntry,
} from "../api/types.ts";
import { primaryLocation } from "./scan-display.ts";

/** Ignore scopes for a selection. A scope is offered only if every selected finding shares it. */

export type IgnoreScopeId = "findings" | "rule" | "directory" | "path" | "advisory" | "kind";

export type IgnoreScope = {
  id: IgnoreScopeId;
  label: string;
  /** Fields matched by this ignore entry. */
  summary: string;
  /** One entry per element; a fingerprint scope writes one per finding. */
  entries: Omit<FindingIgnoreEntry, "reason">[];
  /** Whether the scope supports an advisory justification. */
  advisory: boolean;
};

function unique(values: (string | undefined)[]): string[] {
  const seen = new Set<string>();
  for (const value of values) {
    const trimmed = value?.trim();
    if (trimmed) seen.add(trimmed);
  }
  return [...seen];
}

function advisoryIds(entry: FindingLedgerEntry): string[] {
  const advisory = entry.finding.properties?.lycaon?.advisory;
  if (!advisory) return [];
  return unique([
    advisory.osv_id,
    ...(advisory.cve_ids ?? []),
    ...(advisory.ghsa_ids ?? []),
  ]);
}

/** The advisory's id, preferring a CVE. */
function primaryAdvisory(entry: FindingLedgerEntry): string | undefined {
  const ids = advisoryIds(entry);
  return ids.find((id) => id.toUpperCase().startsWith("CVE-")) ?? ids[0];
}

function directoryOf(path: string): string | undefined {
  const cut = path.lastIndexOf("/");
  return cut > 0 ? path.slice(0, cut) : undefined;
}

/** A scope requires the same nonempty value on every entry. */
function sharedBy<T>(
  entries: readonly T[],
  pick: (entry: T) => string | undefined,
): string | undefined {
  let agreed: string | undefined;
  for (const entry of entries) {
    const value = pick(entry)?.trim();
    if (!value) return undefined;
    if (agreed === undefined) agreed = value;
    else if (agreed !== value) return undefined;
  }
  return agreed;
}

export function ignoreScopesFor(entries: readonly FindingLedgerEntry[]): IgnoreScope[] {
  if (entries.length === 0) return [];
  const paths = entries.map((entry) => primaryLocation(entry.finding)?.uri ?? "");
  const scopes: IgnoreScope[] = [];

  scopes.push({
    id: "findings",
    label: entries.length === 1 ? "This finding only" : `These ${entries.length} findings only`,
    summary: "fingerprint",
    entries: entries.map((entry) => ({
      fingerprint: entry.finding.fingerprints.primary,
      scanner: entry.scanner_id,
    })),
    advisory: false,
  });

  const rule = sharedBy(entries, (entry) => entry.finding.rule_id);
  const scanner = sharedBy(entries, (entry) => entry.scanner_id);
  if (rule && scanner) {
    scopes.push({
      id: "rule",
      label: `Every finding from ${rule}`,
      summary: `rule: ${rule} · scanner: ${scanner}`,
      entries: [{ rule, scanner }],
      advisory: false,
    });
  }

  const file = sharedBy(paths, (path) => path);
  if (file) {
    scopes.push({
      id: "path",
      label: `Everything in ${file}`,
      summary: `path: ${file}`,
      entries: [{ path: file }],
      advisory: false,
    });
  }

  const directory = sharedBy(paths, directoryOf);
  if (directory) {
    scopes.push({
      id: "directory",
      label: `Everything under ${directory}/`,
      summary: `path: ${directory}/**`,
      entries: [{ path: `${directory}/**` }],
      advisory: false,
    });
  }

  const advisory = sharedBy(entries, primaryAdvisory);
  if (advisory) {
    scopes.push({
      id: "advisory",
      label: `Every report of ${advisory}`,
      summary: `advisory: ${advisory}`,
      entries: [{ advisory }],
      advisory: true,
    });
  }

  const kind = sharedBy(entries, (entry) => entry.finding.properties?.lycaon?.kind);
  if (kind && entries.length > 1) {
    scopes.push({
      id: "kind",
      label: `Every ${kind} finding`,
      summary: `kind: ${kind}`,
      entries: [{ kind: kind as FindingKind }],
      advisory: false,
    });
  }

  return scopes;
}

/** Expiry choices in days; null means no expiry. */
export const IGNORE_EXPIRY_CHOICES: readonly { label: string; days: number | null }[] = [
  { label: "30 days", days: 30 },
  { label: "90 days", days: 90 },
  { label: "180 days", days: 180 },
  { label: "Never", days: null },
];

/** Expiry date in YYYY-MM-DD format. */
export function expiryDate(days: number | null, today: Date): string | undefined {
  if (days == null) return undefined;
  const when = new Date(today.getTime());
  when.setUTCDate(when.getUTCDate() + days);
  return when.toISOString().slice(0, 10);
}

/** The YAML the panel is about to write. */
export function ignoreYamlPreview(
  entries: readonly FindingIgnoreEntry[],
  path?: string,
): string {
  if (entries.length === 0) return "";
  const lines = [`# ${path?.trim() || "ignores.yaml"}`, "version: 1", "findings:"];
  for (const entry of entries) {
    const fields: [string, string | undefined][] = [
      ["path", entry.path],
      ["kind", entry.kind],
      ["scanner", entry.scanner],
      ["rule", entry.rule],
      ["advisory", entry.advisory],
      ["fingerprint", entry.fingerprint],
      ["reason", entry.reason],
      ["justification", entry.justification],
      ["expires", entry.expires_on],
    ];
    let first = true;
    for (const [key, value] of fields) {
      if (!value?.trim()) continue;
      lines.push(`${first ? "  - " : "    "}${key}: ${value.trim()}`);
      first = false;
    }
  }
  return lines.join("\n");
}
