import { describe, expect, it } from "vitest";
import type { ApprovalGrant } from "../../api/types.ts";
import { allApprovalGrantCategories } from "../../api/enum-registries.ts";
import {
  expiredGrants,
  filterApprovalGrants,
  formatGrantExpiry,
  GRANT_CATEGORY_ORDER,
  grantCategoryFilterOptions,
  groupApprovalGrants,
  hasElevatedEffects,
  projectApprovalGrants,
  projectAskQuiets,
  unavailableGrants,
} from "./approval-grants-model.ts";
import { GRANT_KIND_LABELS } from "./saved-approvals-copy.ts";

function grant(overrides: Partial<ApprovalGrant> & { id: string }): ApprovalGrant {
  return {
    title: overrides.title ?? "Grant",
    scope: overrides.scope ?? "project",
    category: overrides.category ?? "tool",
    pattern: overrides.pattern ?? "read",
    coverage: overrides.coverage ?? "the read tool",
    granted_at: overrides.granted_at ?? "2026-01-01T00:00:00Z",
    expires_when: overrides.expires_when ?? "in 7 days",
    reask_when: overrides.reask_when ?? "the project changes",
    ...overrides,
  };
}

describe("grant kind coverage of the wire enum", () => {
  // Union-typed arrays do not enforce exhaustive category coverage.
  it("orders and labels every lease category", () => {
    const ordered = new Set<string>(GRANT_CATEGORY_ORDER);
    for (const category of allApprovalGrantCategories()) {
      expect(ordered.has(category), `no chip order for ${category}`).toBe(true);
      expect(
        GRANT_KIND_LABELS[category]?.length ?? 0,
        `no product label for ${category}`,
      ).toBeGreaterThan(0);
    }
    expect(ordered.size).toBe(allApprovalGrantCategories().length);
  });
});

describe("project approval inventory", () => {
  it("includes only the current chat, this project, applicable device leases, and host-selected workers", () => {
    const rows = [
      grant({ id: "chat", scope: "chat", chat_session_id: "current" }),
      grant({ id: "worker", scope: "chat", chat_session_id: "worker" }),
      grant({ id: "other-chat", scope: "chat", chat_session_id: "other" }),
      grant({ id: "project", scope: "project", project_id: "current-project" }),
      grant({ id: "other-project", scope: "project", project_id: "other-project" }),
      grant({ id: "device", scope: "device" }),
      grant({ id: "project-device", scope: "device", project_id: "current-project" }),
      grant({ id: "other-project-device", scope: "device", project_id: "other-project" }),
    ];
    expect(projectApprovalGrants(rows, "current-project", "current", new Set(["worker"]))
      .map((row) => row.id)).toEqual(["chat", "worker", "project", "device", "project-device"]);
    expect(projectApprovalGrants(rows, "current-project", undefined, new Set())
      .map((row) => row.id)).toEqual(["project", "device", "project-device"]);
  });

  it("includes host-selected worker quiets without showing unrelated chats", () => {
    const quiets = [
      { id: "quiet_chat", chat_session_id: "current" },
      { id: "quiet_worker", chat_session_id: "worker" },
      { id: "quiet_other", chat_session_id: "other" },
    ].map((row) => ({ ...row, key: "gate", label: "Quieted ask", suppressed: 0, created_at: "2026-01-01T00:00:00Z" }));
    expect(projectAskQuiets(quiets, "current", new Set(["quiet_worker"])).map((row) => row.id))
      .toEqual(["quiet_chat", "quiet_worker"]);
  });

  it("uses only the host's elevated-effect field to classify priority rows", () => {
    expect(hasElevatedEffects(grant({ id: "ordinary", title: "Host execution" }))).toBe(false);
    expect(hasElevatedEffects(grant({ id: "elevated", elevated_effects: ["host_execution"] }))).toBe(true);
  });
});

describe("grantCategoryFilterOptions", () => {
  it("derives chips from present rows only, with counts and the all sentinel", () => {
    const rows = [
      grant({ id: "a", category: "tool" }),
      grant({ id: "b", category: "tool" }),
      grant({ id: "c", category: "socket_path", pattern: "/tmp/x.sock" }),
    ];
    expect(grantCategoryFilterOptions(rows)).toEqual([
      { id: "all", count: 3 },
      { id: "socket_path", count: 1 },
      { id: "tool", count: 2 },
    ]);
  });

  it("offers only the all chip for an empty list", () => {
    expect(grantCategoryFilterOptions([])).toEqual([{ id: "all", count: 0 }]);
  });
});

describe("filterApprovalGrants", () => {
  const rows: ApprovalGrant[] = [
    grant({ id: "a", title: "Allow read for this project", category: "tool" }),
    grant({
      id: "b",
      title: "Write root /tmp/gone",
      category: "write_root",
      pattern: "/tmp/gone",
      unavailable: true,
    }),
    grant({
      id: "c",
      title: "Planned verification calls",
      category: "action_set",
      pattern: "exact-action-digest",
      action_count: 2,
    }),
    grant({
      id: "d",
      title: "Host resources",
      category: "host_resource",
      pattern: "screen.capture,audio.input",
      resource_ids: ["screen.capture", "audio.input"],
    }),
    grant({
      id: "e",
      title: "Chat lease",
      scope: "chat",
      session_title: "Wire LocalStack into the harness",
    }),
  ];

  it("matches search across title, pattern, category, and structured fields", () => {
    expect(filterApprovalGrants(rows, "WRITE_ROOT", "all").map((g) => g.id)).toEqual(["b"]);
    expect(filterApprovalGrants(rows, "verification", "all").map((g) => g.id)).toEqual(["c"]);
    expect(filterApprovalGrants(rows, "audio.input", "all").map((g) => g.id)).toEqual(["d"]);
    expect(filterApprovalGrants(rows, "localstack", "all").map((g) => g.id)).toEqual(["e"]);
  });

  it("filters by category", () => {
    expect(filterApprovalGrants(rows, "", "action_set").map((g) => g.id)).toEqual(["c"]);
    expect(filterApprovalGrants(rows, "", "write_root").map((g) => g.id)).toEqual(["b"]);
  });
});

describe("groupApprovalGrants", () => {
  it("groups chat rows per chat, then projects by dir, then device", () => {
    const rows: ApprovalGrant[] = [
      grant({ id: "dev1", scope: "device", granted_at: "2026-01-05T00:00:00Z" }),
      grant({
        id: "t1",
        scope: "chat",
        chat_session_id: "sess-1",
        session_title: "Chat one",
        granted_at: "2026-01-02T00:00:00Z",
      }),
      grant({
        id: "t2",
        scope: "chat",
        chat_session_id: "sess-2",
        session_title: "Chat two",
        granted_at: "2026-01-04T00:00:00Z",
      }),
      grant({
        id: "p1",
        scope: "project",
        project_id: "proj-b",
        project_dir: "/b/project",
        granted_at: "2026-01-01T00:00:00Z",
      }),
      grant({
        id: "p2",
        scope: "project",
        project_id: "proj-a",
        project_dir: "/a/project",
        granted_at: "2026-01-03T00:00:00Z",
      }),
      grant({
        id: "t3",
        scope: "chat",
        chat_session_id: "sess-1",
        granted_at: "2026-01-06T00:00:00Z",
      }),
    ];
    const groups = groupApprovalGrants(rows);
    expect(
      groups.map((g) => ({
        kind: g.kind,
        ids: g.grants.map((row) => row.id),
      })),
    ).toEqual([
      { kind: "chat", ids: ["t3", "t1"] },
      { kind: "chat", ids: ["t2"] },
      { kind: "project", ids: ["p2"] },
      { kind: "project", ids: ["p1"] },
      { kind: "device", ids: ["dev1"] },
    ]);
    const first = groups[0]!;
    expect(first.kind === "chat" && first.sessionId).toBe("sess-1");
    // The title survives even when the newest row lacks one.
    expect(first.kind === "chat" && first.sessionTitle).toBe("Chat one");
  });

  it("keeps one band per project when its folder changed between grants", () => {
    const groups = groupApprovalGrants([
      grant({
        id: "before",
        scope: "project",
        project_id: "proj-1",
        project_dir: "/old/place",
        granted_at: "2026-01-01T00:00:00Z",
      }),
      grant({
        id: "after",
        scope: "project",
        project_id: "proj-1",
        project_dir: "/new/place",
        granted_at: "2026-01-02T00:00:00Z",
      }),
    ]);
    expect(groups).toHaveLength(1);
    const band = groups[0]!;
    expect(band.kind === "project" && band.projectId).toBe("proj-1");
    expect(band.grants.map((row) => row.id)).toEqual(["after", "before"]);
  });

  it("keeps folderless projects apart", () => {
    const groups = groupApprovalGrants([
      grant({ id: "a", scope: "project", project_id: "proj-a" }),
      grant({ id: "b", scope: "project", project_id: "proj-b" }),
    ]);
    expect(groups.map((g) => g.kind === "project" && g.projectId)).toEqual([
      "proj-a",
      "proj-b",
    ]);
    expect(groups.every((g) => g.kind === "project" && g.projectDir === "")).toBe(
      true,
    );
  });
});

describe("row state helpers", () => {
  const rows: ApprovalGrant[] = [
    grant({ id: "ok" }),
    grant({ id: "gone", unavailable: true }),
    grant({ id: "old", expired: true, expires_at: "2026-01-01T00:00:00Z" }),
  ];

  it("lists unavailable grants", () => {
    expect(unavailableGrants(rows).map((g) => g.id)).toEqual(["gone"]);
  });

  it("lists expired grants", () => {
    expect(expiredGrants(rows).map((g) => g.id)).toEqual(["old"]);
  });
});

describe("formatGrantExpiry", () => {
  const now = Date.parse("2026-08-04T12:00:00Z");
  const iso = (offsetMinutes: number) =>
    new Date(now + offsetMinutes * 60_000).toISOString();

  it("counts down near-term expiries — the hour rung never reads as wall-clock", () => {
    expect(formatGrantExpiry(iso(54), now)).toBe("in 54 min");
    expect(formatGrantExpiry(iso(60), now)).toBe("in 1 h");
    expect(formatGrantExpiry(iso(85), now)).toBe("in 1 h 25 min");
  });

  it("uses the absolute date once expiry is days out", () => {
    expect(formatGrantExpiry(iso(7 * 24 * 60), now)).not.toContain("in ");
  });

  it("falls back to the absolute date when already past — the badge carries state", () => {
    expect(formatGrantExpiry(iso(-5), now)).not.toContain("in ");
  });

  it("returns a dash for malformed timestamps", () => {
    expect(formatGrantExpiry("not-a-date", now)).toBe("—");
  });
});
