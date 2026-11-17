import { readFileSync } from "node:fs";
import { join } from "node:path";

import { loadSourceCorpus } from "../../test/source-corpus.ts";

const REPO_ROOT = join(import.meta.dirname, "../../../..");

const SCHEMA_DIR = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/tools/schemas",
);
const OPERATION_SCHEMA_DIR = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/tools/operation-schemas",
);
const CATALOG_PATH = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/tools/lycaon-tools.yaml",
);
const EVIDENCE_KINDS_PATH = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/guidance/evidence-kinds.yaml",
);

function namesFromYamlList(text: string): string[] {
  return [...text.matchAll(/^  - ([a-z_]+)/gm)].map((match) => match[1]!);
}

/** Native + boot-registered tools with JSON schemas. */
export function toolSchemaNames(): string[] {
  return loadSourceCorpus(SCHEMA_DIR, { extensions: [".yaml"] }).files
    .map((file) => file.rel.replace(/\.yaml$/, ""))
    .sort();
}

/** Native operation identities that may appear in older direct-tool transcripts. */
export function toolOperationSchemaNames(): string[] {
  return loadSourceCorpus(OPERATION_SCHEMA_DIR, { extensions: [".yaml"] }).files
    .map((file) => file.rel.replace(/\.yaml$/, ""))
    .sort();
}

/** Catalog opt-in / coordinator profile tools from lycaon-tools.yaml. */
export function lycaonCatalogToolNames(): string[] {
  return namesFromYamlList(readFileSync(CATALOG_PATH, "utf8"));
}

let cachedToolEvidenceKindMap: Readonly<Record<string, string>> | undefined;

/** Tool name → host evidence kind from evidence-kinds.yaml (produces_evidence tools). */
export function toolEvidenceKindMap(): Readonly<Record<string, string>> {
  if (cachedToolEvidenceKindMap) return cachedToolEvidenceKindMap;
  const text = readFileSync(EVIDENCE_KINDS_PATH, "utf8");
  const toolsStart = text.indexOf("\ntools:");
  const slice = toolsStart >= 0 ? text.slice(toolsStart) : text;
  const map: Record<string, string> = {};
  for (const match of slice.matchAll(
    /^  ([a-z_]+):\s+\{ produces_evidence: \{ kind: ([a-z_]+)/gm,
  )) {
    map[match[1]!] = match[2]!;
  }
  cachedToolEvidenceKindMap = map;
  return map;
}
