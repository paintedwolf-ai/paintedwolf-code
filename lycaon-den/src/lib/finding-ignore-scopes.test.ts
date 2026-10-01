import { describe, expect, it } from "vitest";
import type { FindingLedgerEntry } from "../api/types.ts";
import {
  expiryDate,
  ignoreScopesFor,
  ignoreYamlPreview,
} from "./finding-ignore-scopes.ts";

function entry(overrides: {
  rule?: string;
  path?: string;
  scanner?: string;
  kind?: string;
  cve?: string;
}): FindingLedgerEntry {
  return {
    finding: {
      rule_id: overrides.rule ?? "opengrep:sql-concat",
      level: "high",
      message: "SQL built via string concat",
      locations: [{ uri: overrides.path ?? "internal/db/query.go", start_line: 12 }],
      fingerprints: { primary: `fp-${overrides.path ?? overrides.rule ?? "a"}` },
      tool: { driver_id: overrides.scanner ?? "opengrep-sast", name: "opengrep" },
      properties: {
        lycaon: {
          kind: overrides.kind,
          ...(overrides.cve
            ? { advisory: { osv_id: "GHSA-x", cve_ids: [overrides.cve] } }
            : {}),
        },
      },
    },
    state: "open",
    scanner_id: overrides.scanner ?? "opengrep-sast",
    first_seen_at: "2026-09-01T10:00:00Z",
    last_seen_at: "2026-09-10T10:00:00Z",
    observations: 1,
  } as unknown as FindingLedgerEntry;
}

describe("ignore scopes", () => {
  // A scope is offered only when every selected finding shares it.
  it("offers only the predicates the whole selection shares", () => {
    const mixed = ignoreScopesFor([
      entry({ rule: "rule-a", path: "src/a.go" }),
      entry({ rule: "rule-b", path: "test/b.go" }),
    ]);
    expect(mixed.map((scope) => scope.id)).toEqual(["findings"]);

    const sameRule = ignoreScopesFor([
      entry({ rule: "rule-a", path: "src/one.go" }),
      entry({ rule: "rule-a", path: "src/two.go" }),
    ]);
    expect(sameRule.map((scope) => scope.id)).toEqual(["findings", "rule", "directory"]);
  });

  // A finding with no value for a scope defeats that scope.
  it("refuses a scope any selected finding cannot supply", () => {
    const scopes = ignoreScopesFor([
      // No advisory and no directory.
      entry({ rule: "rule-lint", path: "package-lock.json" }),
      entry({ rule: "rule-sast", path: "internal/api/authz.go", cve: "CVE-2026-1" }),
    ]);
    expect(scopes.map((scope) => scope.id)).toEqual(["findings"]);
  });

  it("offers a scope when every finding supplies the same value", () => {
    const scopes = ignoreScopesFor([
      entry({ rule: "rule-a", path: "internal/api/one.go", cve: "CVE-2026-1" }),
      entry({ rule: "rule-a", path: "internal/api/two.go", cve: "CVE-2026-1" }),
    ]);
    expect(scopes.map((scope) => scope.id)).toEqual([
      "findings",
      "rule",
      "directory",
      "advisory",
    ]);
  });

  it("writes one entry per finding for a fingerprint scope, one otherwise", () => {
    const scopes = ignoreScopesFor([
      entry({ rule: "rule-a", path: "src/one.go" }),
      entry({ rule: "rule-a", path: "src/two.go" }),
    ]);
    const findings = scopes.find((scope) => scope.id === "findings");
    expect(findings?.entries).toHaveLength(2);
    expect(scopes.find((scope) => scope.id === "rule")?.entries).toHaveLength(1);
  });

  // A rule scope also names the scanner.
  it("pins a rule scope to the scanner whose vocabulary it is", () => {
    const scopes = ignoreScopesFor([entry({ rule: "rule-a", scanner: "gitleaks" })]);
    const rule = scopes.find((scope) => scope.id === "rule");
    expect(rule?.entries[0]).toMatchObject({ rule: "rule-a", scanner: "gitleaks" });
  });

  // Only advisory scopes accept a VEX justification.
  it("marks the advisory scope as the one that can carry a justification", () => {
    const scopes = ignoreScopesFor([entry({ cve: "CVE-2026-21102" })]);
    const advisory = scopes.find((scope) => scope.id === "advisory");
    expect(advisory?.advisory).toBe(true);
    expect(advisory?.entries[0]).toMatchObject({ advisory: "CVE-2026-21102" });
    expect(scopes.filter((scope) => scope.advisory)).toHaveLength(1);
  });

  it("dates an expiry as a calendar day, and never for a decision without one", () => {
    const today = new Date("2026-09-10T23:59:00Z");
    expect(expiryDate(90, today)).toBe("2026-12-09");
    expect(expiryDate(null, today)).toBeUndefined();
  });

  // Preview renders the YAML to be written.
  it("previews the entries in the file's own shape", () => {
    const yaml = ignoreYamlPreview(
      [
      { path: "test/**", reason: "fixture material", expires_on: "2026-12-09" },
      {
        advisory: "CVE-2026-21102",
        reason: "the affected codec is never reached",
        justification: "vulnerable_code_not_in_execute_path",
      },
      ],
      "/repo/.paintedwolf/ignores.yaml",
    );
    expect(yaml).toContain("# /repo/.paintedwolf/ignores.yaml");
    expect(yaml).toContain("version: 1");
    expect(yaml).toContain("  - path: test/**");
    expect(yaml).toContain("    reason: fixture material");
    expect(yaml).toContain("    expires: 2026-12-09");
    expect(yaml).toContain("  - advisory: CVE-2026-21102");
    expect(yaml).toContain("    justification: vulnerable_code_not_in_execute_path");
  });
});
