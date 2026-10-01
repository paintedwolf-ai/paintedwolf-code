import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export type ForbiddenDenPattern = {
  id: string;
  pattern: string;
  paths: string;
  exclude?: string;
};

export type DenHostAuthorityRegistry = {
  session_decision_fields: string[];
  workflow_run_decision_fields: string[];
  tool_decision_fields: string[];
  worker_decision_fields: string[];
  openapi_schemas: Record<string, string[]>;
  spawn_policy: {
    forbidden_branch_substrings: string[];
    catalog_only_note: string;
  };
  sse_authority_hydration: Record<string, string[]>;
  sse_patch_only_topics: string[];
  forbidden_den_patterns: ForbiddenDenPattern[];
  decision_surface_files: string[];
  display_only_files: string[];
  transport_allowlist: string[];
  /** Platform modules allowed to import the native bridge. */
  platform_tauri_modules: string[];
};

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "../../../..");

export function denHostAuthorityRegistryPath(): string {
  return join(
    repoRoot,
    "lycaon/test/contract/testdata/den_host_authority.json",
  );
}

export function loadDenHostAuthorityRegistry(): DenHostAuthorityRegistry {
  const raw = readFileSync(denHostAuthorityRegistryPath(), "utf8");
  return JSON.parse(raw) as DenHostAuthorityRegistry;
}

export function repoRootDir(): string {
  return repoRoot;
}
