import type { ScannerCatalogEntry, ScannerSummary } from "../../api/types.ts";

/** Scanner slots, in display order. Exactly one scanner is enabled per slot. */
export const SCANNER_SLOTS = ["sast", "sca", "secret"] as const;
export type ScannerSlot = (typeof SCANNER_SLOTS)[number];

/**
 * The slot a scanner competes for: its first category that is not the generic
 * "security" tag, which nearly every security scanner carries.
 */
function primaryCategory(categories: readonly string[]): string {
  for (const category of categories) {
    if (category && category !== "security") return category;
  }
  return "";
}

const SLOT_JOBS: Partial<Record<string, string>> = {
  sast: "code scanner",
  sca: "dependency scanner",
  secret: "secret scanner",
};

type NamedScanner = { id: string; label?: string; categories?: readonly string[] };

function catalogLabel(scanner: NamedScanner): string {
  return scanner.label?.trim() || scanner.id;
}

/**
 * Names a scanner by the job it does. An engine name reads only beside the job
 * it fills, so Settings shows catalog labels and every other surface shows this.
 * A scanner outside the three jobs keeps its catalog label.
 */
export function scannerJobLabel(scanner: NamedScanner): string {
  const job = SLOT_JOBS[primaryCategory(scanner.categories ?? [])];
  return job ? job.charAt(0).toUpperCase() + job.slice(1) : catalogLabel(scanner);
}

/** The same name inside a sentence: "the code scanner". */
export function scannerJobPhrase(scanner: NamedScanner): string {
  const job = SLOT_JOBS[primaryCategory(scanner.categories ?? [])];
  return job ? `the ${job}` : catalogLabel(scanner);
}

export function scannerMissingBinary(scanner: ScannerSummary): boolean {
  return scanner.issues?.includes("binary_missing") ?? false;
}

/** A scanner is ours (shipped) rather than a tool the user installed. */
function isBuiltIn(scanner: ScannerSummary): boolean {
  return scanner.driver === "library" || scanner.driver === "bundled";
}

export type SlotCandidate = {
  id: string;
  label: string;
  /** Engine and rule source behind this option. */
  description: string;
  /** Present once the scanner is in the user's catalog. */
  scanner?: ScannerSummary;
  /** Present when we know how to drive this tool but it may not be added yet. */
  known?: ScannerCatalogEntry;
  builtIn: boolean;
  current: boolean;
  /** Selectable only when the tool can actually run here. */
  installed: boolean;
};

/**
 * Everything that could take one slot: installed scanners first (ours leading),
 * then known tools the user has not added. Not-installed rows stay visible so
 * the picker answers "what else could go here", not just "what is here".
 */
export function slotCandidates(
  slot: ScannerSlot,
  scanners: readonly ScannerSummary[],
  known: readonly ScannerCatalogEntry[],
): SlotCandidate[] {
  const out: SlotCandidate[] = [];
  const seen = new Set<string>();

  for (const scanner of scanners) {
    if (primaryCategory(scanner.categories) !== slot) continue;
    seen.add(scanner.id);
    const entry = known.find((k) => k.id === scanner.id);
    out.push({
      id: scanner.id,
      label: scanner.label?.trim() || scanner.id,
      description: scanner.description?.trim() || entry?.hint?.trim() || "",
      scanner,
      known: entry,
      builtIn: isBuiltIn(scanner),
      current: scanner.enabled,
      installed: isBuiltIn(scanner) || !scannerMissingBinary(scanner),
    });
  }

  for (const entry of known) {
    if (seen.has(entry.id)) continue;
    if (primaryCategory(entry.categories) !== slot) continue;
    out.push({
      id: entry.id,
      label: entry.label,
      description: entry.hint?.trim() || "",
      known: entry,
      builtIn: false,
      current: false,
      installed: entry.binary_found,
    });
  }

  return out.sort((a, b) => Number(b.builtIn) - Number(a.builtIn));
}
