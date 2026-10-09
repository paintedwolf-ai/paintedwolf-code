import { execFileSync, execSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { openAPISchemaProperties, readOpenAPIBundle } from "../api/openapi-sync.ts";
import {
  loadDenHostAuthorityRegistry,
  repoRootDir,
} from "./connection/host-authority-registry.ts";

const repoRoot = repoRootDir();
const denSrc = join(repoRoot, "lycaon-den/src");
const registry = loadDenHostAuthorityRegistry();

function rg(pattern: string, paths: string, exclude?: string): string {
  const pathList = paths
    .split("|")
    .map((p) => p.trim())
    .filter(Boolean);
  const args = ["-n", pattern, ...pathList];
  if (exclude) {
    for (const glob of exclude.split("|")) {
      const trimmed = glob.trim();
      if (trimmed) args.push("-g", `!${trimmed}`);
    }
  }
  try {
    return execFileSync("rg", args, { encoding: "utf8", cwd: repoRoot }).trim();
  } catch (err: unknown) {
    const status = (err as { status?: number }).status;
    if (status === 1) return "";
    throw err;
  }
}

describe("den host authority registry", () => {
  it("registry OpenAPI fields exist in bundled spec", () => {
    const yaml = readOpenAPIBundle();
    for (const [schema, fields] of Object.entries(registry.openapi_schemas)) {
      const props = openAPISchemaProperties(yaml, schema);
      for (const field of fields) {
        expect(props, `${schema}.${field}`).toContain(field);
      }
    }
  });

  for (const rule of registry.forbidden_den_patterns) {
    it(`forbids ${rule.id}`, () => {
      expect(rg(rule.pattern, rule.paths, rule.exclude)).toBe("");
    });
  }

  it("forbids Rejected: parsing for tool card status", () => {
    expect(rg("Rejected:", join(denSrc, "chat/tool/tool-part-model.ts"))).toBe("");
  });

  it("keeps prose rejection markers out of markdown authority decisions", () => {
    expect(
      rg("\\bREJECTED\\b", join(denSrc, "chat/markdown/markdown-output.ts")),
    ).toBe("");
  });

  it("decision surfaces reference host authority fields", () => {
    const surfaces = registry.decision_surface_files.map((rel) =>
      join(repoRoot, rel),
    );
    const combined = surfaces
      .map((path) => readFileSync(path, "utf8"))
      .join("\n");
    const requiredSnippets = [
      "ui?.pending_workflow_start",
      "ui?.human_approval_awaiting",
      "tool_result?.outcome",
      "tool_result?.dispatch",
      "job_id",
    ];
    for (const snippet of requiredSnippets) {
      expect(combined, `missing ${snippet}`).toContain(snippet);
    }
  });

  it("spawn policy fields are not used for Den branching", () => {
    for (const sub of registry.spawn_policy.forbidden_branch_substrings) {
      const hits = rg(sub, `${denSrc}/components`, "types\\.ts|\\.test\\.");
      expect(hits, sub).toBe("");
    }
  });

  it("transport uses fetch only through api/ and backend health", () => {
    // \b anchors the match so a createResource `refetch()` is not read as transport.
    const out = execSync(`rg -l '\\bfetch\\(' ${denSrc} -g '!*.test.*' 2>/dev/null || true`, {
      encoding: "utf8",
      cwd: repoRoot,
    }).trim();
    const files = out ? out.split("\n") : [];
    for (const file of files) {
      const rel = file.includes("lycaon-den/src/")
        ? file.split("lycaon-den/src/")[1]!
        : file;
      expect(rel.startsWith("api/") || rel === "platform/connection/backend.ts", `unexpected fetch in ${rel}`).toBe(true);
    }
  });

  it("enumerates platform Tauri modules including drop bridge", () => {
    const required = [
      "lycaon-den/src/platform/files/file-drop.ts",
      "lycaon-den/src/platform/files/read-path-bytes.ts",
      "lycaon-den/src/platform/files/path-kind.ts",
    ];
    for (const mod of required) {
      expect(registry.platform_tauri_modules).toContain(mod);
      expect(existsSync(join(repoRoot, mod)), mod).toBe(true);
    }
  });

  it("drop-bridge platform modules are the only new @tauri-apps drop imports", () => {
    for (const rel of [
      "platform/files/file-drop.ts",
      "platform/files/read-path-bytes.ts",
      "platform/files/path-kind.ts",
    ]) {
      const src = readFileSync(join(denSrc, rel), "utf8");
      expect(src).toMatch(/@tauri-apps/);
    }
    expect(
      rg(
        "@tauri-apps",
        `${denSrc}/chat|${denSrc}/components|${denSrc}/workflow`,
        "\\.test\\.",
      ),
    ).not.toMatch(/file-drop|read-path-bytes|path-kind/);
  });

  it("app-connection hydrates SSE authority keys", () => {
    const events = readFileSync(
      join(denSrc, "platform/connection/connection-events.ts"),
      "utf8",
    );
    expect(events).toContain("onInvalidate: (keys, scope) => ports.invalidation.invalidate(");
    const src = readFileSync(
      join(denSrc, "platform/connection/connection-invalidation.ts"),
      "utf8",
    );
    for (const [key, hydrators] of Object.entries(
      registry.sse_authority_hydration,
    )) {
      expect(src, `missing keys.includes("${key}")`).toContain(
        `keys.includes("${key}")`,
      );
      for (const hydrate of hydrators) {
        expect(src, `${key} -> ${hydrate}`).toContain(hydrate);
      }
    }
  });

  it("message topic does not invalidate full transcript via session key", () => {
    const events = readFileSync(join(denSrc, "api/events.ts"), "utf8");
    const messageBlock = events.slice(
      events.indexOf('case "message":'),
      events.indexOf('case "checkpoint":'),
    );
    expect(messageBlock).not.toContain("scheduleMessages");
    expect(registry.sse_patch_only_topics).toContain("message");
  });

  it("AppState has no local workflow gate flags", () => {
    const src = ["app-state.ts", "session-activity-actions.ts", "session-coordination-actions.ts", "git-cache-actions.ts"]
      .map((file) => readFileSync(join(denSrc, "store", file), "utf8")).join("\n");
    for (const forbidden of [
      "planApproved",
      "gateBlocked",
      "spawnAllowlist",
      "handoffReady",
    ]) {
      expect(src).not.toContain(forbidden);
    }
  });

  it("matchWorkerForTask does not fuzzy-match agent_type or prompt", () => {
    expect(
      rg(
        "subagentType\\(part\\)|taskDescription\\(part\\)|prompt\\.includes",
        join(denSrc, "chat/worker/workers-model.ts"),
      ),
    ).toBe("");
  });

  it("worker-completion-envelope module deleted", () => {
    expect(
      existsSync(join(denSrc, "chat/worker-completion-envelope.ts")),
    ).toBe(false);
  });

  it("worker transcript display uses markdown-output only", () => {
    const markdown = readFileSync(
      join(denSrc, "chat/markdown/markdown-output.ts"),
      "utf8",
    );
    expect(markdown).toContain("workerTranscriptMarkdownSource");
    const transcript = readFileSync(
      join(denSrc, "components/transcript/WorkerTranscriptRows.tsx"),
      "utf8",
    );
    expect(transcript).toContain("markdown-output.ts");
    expect(transcript).not.toContain("worker-completion-envelope");
  });

  it("transcript visibility uses wire field not content prefix", () => {
    const src = readFileSync(join(denSrc, "chat/transcript/projection/transcript-items.ts"), "utf8");
    expect(src).toContain('msg.visibility === "internal"');
    expect(src).not.toMatch(/\[host:/);
    expect(src).not.toContain("isHostInjectedUserMessage");
  });
});
