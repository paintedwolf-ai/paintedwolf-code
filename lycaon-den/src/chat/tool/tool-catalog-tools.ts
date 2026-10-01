import { readFileSync } from "node:fs";
import { join } from "node:path";

import { loadSourceCorpus } from "../../test/source-corpus.ts";
import { TASK_DISPATCH_TOOLS } from "./tool-presentation.generated.ts";

const REPO_ROOT = join(import.meta.dirname, "../../../..");

const SCHEMA_DIR = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/tools/schemas",
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

/** Catalog opt-in / coordinator profile tools from lycaon-tools.yaml. */
export function lycaonCatalogToolNames(): string[] {
  return namesFromYamlList(readFileSync(CATALOG_PATH, "utf8"));
}

/** Catalog tools rendered through StructuredToolBody. */
export function genericTranscriptToolNames(): string[] {
  const names = new Set<string>([
    ...toolSchemaNames(),
    ...lycaonCatalogToolNames(),
  ]);
  for (const tool of TASK_DISPATCH_TOOLS) {
    names.delete(tool);
  }
  return [...names].sort();
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
