import { assert, describe, expect, it } from "vitest";
import type { ManagedSecret } from "../../api/types.ts";
import {
  DEFAULT_SECRET_FILTER,
  addDraftProblem,
  canHold,
  canPromote,
  filterSecrets,
  hiddenRevokedCount,
  isRevoked,
  labelProblem,
  needsValue,
  resolveAgentUseDeadline,
  sortSecretsByNewest,
  valueProblem,
} from "./managed-secrets-model.ts";

function secret(overrides: Partial<ManagedSecret> = {}): ManagedSecret {
  return {
    reference: "{{paintedwolf-secret:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}}",
    name: "Registry token",
    purpose: "Publishes packages",
    scope: "project",
    origin: "generated",
    created_at: "2026-08-31T10:00:00Z",
    state: "active",
    version: 1,
    use_count: 0,
    reveal_count: 0,
    ...overrides,
  };
}

const NOW = new Date("2026-09-01T12:00:00Z");

describe("secret filtering", () => {
  const items = [
    secret({ name: "Live key" }),
    secret({ name: "Dead key", state: "revoked" }),
    secret({ name: "Lapsed key", state: "agent_use_expired" }),
    secret({ name: "Entered key", origin: "settings_entered" }),
  ];

  it("hides revoked material by default and says how much is hidden", () => {
    expect(filterSecrets(items, DEFAULT_SECRET_FILTER).map((s) => s.name)).toEqual([
      "Live key",
      "Lapsed key",
      "Entered key",
    ]);
    expect(hiddenRevokedCount(items, DEFAULT_SECRET_FILTER)).toBe(1);
  });

  it("reports nothing hidden once the filter stops hiding it", () => {
    const showAll = { ...DEFAULT_SECRET_FILTER, state: "all" as const };
    expect(filterSecrets(items, showAll)).toHaveLength(4);
    expect(hiddenRevokedCount(items, showAll)).toBe(0);
  });

  it("narrows to one creation origin", () => {
    const entered = { ...DEFAULT_SECRET_FILTER, origin: "settings_entered" as const };
    expect(filterSecrets(items, entered).map((s) => s.name)).toEqual(["Entered key"]);
  });

  it("searches name and purpose together", () => {
    const byPurpose = { ...DEFAULT_SECRET_FILTER, query: "publishes" };
    expect(filterSecrets(items, byPurpose)).toHaveLength(3);
    const byName = { ...DEFAULT_SECRET_FILTER, query: "lapsed" };
    expect(filterSecrets(items, byName).map((s) => s.name)).toEqual(["Lapsed key"]);
  });

  it("counts only the revoked rows the other filters would have kept", () => {
    const scoped = { ...DEFAULT_SECRET_FILTER, query: "live" };
    expect(hiddenRevokedCount(items, scoped)).toBe(0);
  });

  it("sorts newest first", () => {
    const ordered = sortSecretsByNewest([
      secret({ name: "older", created_at: "2026-08-01T00:00:00Z" }),
      secret({ name: "newer", created_at: "2026-08-30T00:00:00Z" }),
    ]);
    expect(ordered.map((s) => s.name)).toEqual(["newer", "older"]);
  });
});

describe("what a state allows", () => {
  it("identifies revoked secrets", () => {
    expect(isRevoked(secret({ state: "revoked" }))).toBe(true);
    for (const state of ["active", "agent_use_expired", "unavailable"] as const) {
      expect(isRevoked(secret({ state }))).toBe(false);
    }
  });

  it("routes a missing value to a restore", () => {
    expect(needsValue(secret({ state: "unavailable" }))).toBe(true);
    expect(needsValue(secret({ state: "agent_use_expired" }))).toBe(false);
  });

  it("offers promotion only for a live chat capability", () => {
    expect(canPromote(secret({ scope: "chat", chat_session_id: "t" }))).toBe(true);
    expect(canPromote(secret({ scope: "project" }))).toBe(false);
    expect(canPromote(secret({ scope: "chat", state: "revoked" }))).toBe(false);
  });

  it("offers a hold only for a live value the host supplied", () => {
    expect(canHold(secret({ custody: "chat" }))).toBe(true);
    expect(canHold(secret({ custody: "host" }))).toBe(true);
    expect(canHold(secret({ custody: "person" }))).toBe(false);
    expect(canHold(secret({ custody: "file", origin: "file_marked" }))).toBe(false);
    expect(canHold(secret({ custody: "host", origin: "cookie_jar" }))).toBe(false);
    expect(canHold(secret({ custody: "chat", state: "revoked" }))).toBe(false);
    expect(canHold(secret({ state: "unavailable" }))).toBe(false);
  });
});

describe("draft validation", () => {
  it("requires a purpose when adding, because the value outlives every chat", () => {
    expect(
      addDraftProblem({ name: "n", purpose: "", value: "long-enough", agentUseEndsAt: "" }),
    ).toBe("purpose_missing");
    expect(labelProblem("n", "", false)).toBeUndefined();
  });

  it("accepts PIN-length values and still refuses an empty value", () => {
    expect(valueProblem("1234")).toBeUndefined();
    expect(valueProblem("")).toBe("value_missing");
    expect(valueProblem("long-enough-value")).toBeUndefined();
  });

  it.each(["a", "é", "狼", "🐺", " "])("measures the recognition floor in Unicode code points for %s", (character) => {
    for (let length = 1; length <= 8; length++) {
      expect(valueProblem(character.repeat(length))).toBe(length < 4 ? "value_too_short" : undefined);
    }
  });

  it.each(["a", "é", "狼", "🐺"])("measures the storage ceiling in encoded bytes for %s", (character) => {
    const bytes = new TextEncoder().encode(character).length;
    const length = Math.floor(65536 / bytes);
    expect(valueProblem(character.repeat(length))).toBeUndefined();
    expect(valueProblem(character.repeat(length + 1))).toBe("value_too_long");
  });

  it("counts a name in characters rather than code units", () => {
    expect(labelProblem("é".repeat(80), "p", false)).toBeUndefined();
    expect(labelProblem("é".repeat(81), "p", false)).toBe("name_too_long");
  });

  it("does not trim the value", () => {
    expect(valueProblem("  padded  ")).toBeUndefined();
  });
});

describe("agent-use deadline choices", () => {
  it("resolves every relative choice against now", () => {
    for (const [choice, at] of [
      ["30d", "2026-10-01T12:00:00.000Z"],
      ["90d", "2026-11-30T12:00:00.000Z"],
      ["1y", "2027-09-01T12:00:00.000Z"],
    ] as const) {
      expect(resolveAgentUseDeadline(choice, "", NOW)).toEqual({ at });
    }
  });

  it("says no deadline with an empty stamp rather than a missing field", () => {
    expect(resolveAgentUseDeadline("never", "", NOW)).toEqual({ at: "" });
  });

  it("refuses a date in the past, beyond a year, or absent", () => {
    expect(resolveAgentUseDeadline("custom", "2026-08-01", NOW)).toEqual({ problem: "date_past" });
    expect(resolveAgentUseDeadline("custom", "2030-01-01", NOW)).toEqual({
      problem: "date_too_far",
    });
    expect(resolveAgentUseDeadline("custom", "", NOW)).toEqual({ problem: "date_missing" });
  });

  it("takes a chosen date as the end of that day", () => {
    const resolved = resolveAgentUseDeadline("custom", "2026-09-30", NOW);
    assert("at" in resolved, "Expected a resolved deadline");
    const local = new Date(resolved.at);
    expect([
      local.getFullYear(), local.getMonth() + 1, local.getDate(),
      local.getHours(), local.getMinutes(), local.getSeconds(), local.getMilliseconds(),
    ]).toEqual([2026, 9, 30, 23, 59, 59, 0]);
  });
});
