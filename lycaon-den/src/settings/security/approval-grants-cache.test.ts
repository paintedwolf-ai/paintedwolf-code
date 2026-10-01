import { stubClient } from "../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ApprovalGrant } from "../../api/types.ts";
import {
  approvalGrants,
  approvalGrantsError,
  approvalQuiets,
  hasGrant,
  hasQuiet,
  invalidateApprovalGrantsCache,
  isApprovalGrantsCacheLoaded,
  loadApprovalGrantsCache,
  retryApprovalGrantsLoad,
  refreshApprovalGrantsCache,
  revokeGrantsOptimistic,
} from "./approval-grants-cache.ts";

function grant(
  id: string,
  category: ApprovalGrant["category"] = "tool",
): ApprovalGrant {
  return {
    id,
    scope: "project",
    category,
    pattern: category === "action_set" ? "exact-action-digest" : "read",
    title: "Allow bounded actions",
    coverage: "only the reviewed action",
    granted_at: "2026-01-01T00:00:00Z",
    expires_when: "in 7 days",
    reask_when: "the action or confinement changes",
  };
}

describe("approval-grants-cache", () => {
  beforeEach(() => {
    invalidateApprovalGrantsCache();
  });

  it("loads once and shares membership across callers", async () => {
    const listApprovalGrants = vi.fn().mockResolvedValue({
      grants: [grant("g1", "action_set")],
    });
    const client = stubClient({ listApprovalGrants });

    await Promise.all([
      loadApprovalGrantsCache(client),
      loadApprovalGrantsCache(client),
    ]);
    expect(listApprovalGrants).toHaveBeenCalledTimes(1);
    expect(isApprovalGrantsCacheLoaded()).toBe(true);
    expect(hasGrant("g1")).toBe(true);
    expect(hasGrant("missing")).toBe(false);
    expect(approvalGrants().map((g) => g.id)).toEqual(["g1"]);

    await loadApprovalGrantsCache(client);
    expect(listApprovalGrants).toHaveBeenCalledTimes(1);
  });

  it("optimistic revoke hides membership then refreshes", async () => {
    const listApprovalGrants = vi
      .fn()
      .mockResolvedValueOnce({
        grants: [grant("g1", "action_set"), grant("g2")],
        quiets: [
          {
            id: "quiet_1",
            chat_session_id: "chat",
            key: "authority_misuse:aws-cli/s3-remove-bucket",
            label: "aws-cli / s3-remove-bucket",
            suppressed: 2,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
      })
      .mockResolvedValueOnce({
        grants: [grant("g2")],
        quiets: [],
      });
    const client = stubClient({ listApprovalGrants });
    await loadApprovalGrantsCache(client);
    expect(hasGrant("g1")).toBe(true);
    expect(hasQuiet("quiet_1")).toBe(true);
    expect(approvalQuiets()).toHaveLength(1);

    revokeGrantsOptimistic(["g1", "quiet_1"]);
    expect(hasGrant("g1")).toBe(false);
    expect(hasQuiet("quiet_1")).toBe(false);
    expect(approvalGrants().map((g) => g.id)).toEqual(["g2"]);
    expect(approvalQuiets()).toEqual([]);

    await vi.waitFor(() => {
      expect(listApprovalGrants).toHaveBeenCalledTimes(2);
      expect(hasGrant("g2")).toBe(true);
      expect(hasGrant("g1")).toBe(false);
    });
  });

  it("invalidate forces a subsequent reload", async () => {
    const listApprovalGrants = vi.fn().mockResolvedValue({
      grants: [grant("g1", "action_set")],
    });
    const client = stubClient({ listApprovalGrants });
    await loadApprovalGrantsCache(client);
    invalidateApprovalGrantsCache();
    expect(isApprovalGrantsCacheLoaded()).toBe(false);
    expect(hasGrant("g1")).toBe(false);
    await loadApprovalGrantsCache(client);
    expect(listApprovalGrants).toHaveBeenCalledTimes(2);
    expect(hasGrant("g1")).toBe(true);
  });

  it("parks a load error until retried", async () => {
    const listApprovalGrants = vi
      .fn()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({ grants: [grant("g1")] });
    const client = stubClient({ listApprovalGrants });

    await loadApprovalGrantsCache(client);
    expect(isApprovalGrantsCacheLoaded()).toBe(false);
    expect(approvalGrantsError()).toBe("offline");

    retryApprovalGrantsLoad();
    expect(approvalGrantsError()).toBeUndefined();
    await loadApprovalGrantsCache(client);
    expect(isApprovalGrantsCacheLoaded()).toBe(true);
    expect(hasGrant("g1")).toBe(true);
  });
  it.each(["invalidate", "refresh", "revoke", "client"] as const)(
    "%s discards an older response without replacing newer membership",
    async (change) => {
      let settleOld!: (value: { grants: ApprovalGrant[] }) => void;
      let settleNew!: (value: { grants: ApprovalGrant[] }) => void;
      const oldResponse = new Promise<{ grants: ApprovalGrant[] }>((resolve) => { settleOld = resolve; });
      const newResponse = new Promise<{ grants: ApprovalGrant[] }>((resolve) => { settleNew = resolve; });
      const client = stubClient({ listApprovalGrants: vi.fn().mockReturnValueOnce(oldResponse).mockReturnValueOnce(newResponse) });
      const oldLoad = loadApprovalGrantsCache(client);
      let current = client;
      if (change === "invalidate") invalidateApprovalGrantsCache();
      if (change === "refresh") refreshApprovalGrantsCache();
      if (change === "revoke") revokeGrantsOptimistic(["old"]);
      if (change === "client") current = stubClient({ listApprovalGrants: vi.fn().mockReturnValue(newResponse) });
      const newLoad = loadApprovalGrantsCache(current);
      settleOld({ grants: [grant("old")] });
      await oldLoad;
      expect(hasGrant("old")).toBe(false);
      // Settling the superseded request must not release the current request.
      const coalesced = loadApprovalGrantsCache(current);
      settleNew({ grants: [grant("current")] });
      await Promise.all([newLoad, coalesced]);
      expect(approvalGrants().map((g) => g.id)).toEqual(["current"]);
    },
  );

});
