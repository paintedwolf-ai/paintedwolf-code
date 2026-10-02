import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { ManagedSecret } from "../../api/types.ts";
import {
  createComposerSecretForMark,
  revokeComposerSecret,
} from "./composer-secret-mark.ts";

const REFERENCE = "{{paintedwolf-secret:123e4567-e89b-12d3-a456-426614174000}}";
const SECRET_ID = "123e4567-e89b-12d3-a456-426614174000";

const created: ManagedSecret = {
  reference: REFERENCE,
  name: "PIN",
  scope: "chat",
  origin: "composer_marked",
  created_at: "2026-09-01T00:00:00Z",
  state: "active",
  version: 1,
  use_count: 0,
  reveal_count: 0,
  release_count: 0,
};

const request = {
  name: "PIN",
  purpose: "test credential",
  secret_value: "x",
  operation_id: "op-1",
};

function clientWith(over: Partial<LycaonClient>) {
  return stubClient(over);
}

describe("createComposerSecretForMark", () => {
  it("returns the capability the host created", async () => {
    const createComposerSecret = vi.fn(async () => created);
    const client = clientWith({ createComposerSecret });

    await expect(
      createComposerSecretForMark(client, "sess-1", "proj-1", request),
    ).resolves.toBe(created);
    expect(createComposerSecret).toHaveBeenCalledTimes(1);
  });

  it("revokes the capability a lost response hid, then reports the failure", async () => {
    const createComposerSecret = vi
      .fn()
      .mockRejectedValueOnce(new Error("timeout"))
      .mockResolvedValueOnce(created);
    const revokeProjectManagedSecret = vi.fn(async () => {});
    const client = clientWith({ createComposerSecret, revokeProjectManagedSecret });

    await expect(
      createComposerSecretForMark(client, "sess-1", "proj-1", request),
    ).rejects.toThrow("timeout");
    // The repeated operation_id identifies the committed capability.
    expect(createComposerSecret).toHaveBeenNthCalledWith(2, "sess-1", request);
    expect(revokeProjectManagedSecret).toHaveBeenCalledWith("proj-1", SECRET_ID);
  });

  it("says a capability may be active when it cannot tell", async () => {
    const createComposerSecret = vi.fn().mockRejectedValue(new Error("offline"));
    const client = clientWith({ createComposerSecret });

    await expect(
      createComposerSecretForMark(client, "sess-1", "proj-1", request),
    ).rejects.toThrow(/cannot tell whether a secret was created/);
  });

  it("reports the orphan when the recovery revoke fails", async () => {
    const createComposerSecret = vi
      .fn()
      .mockRejectedValueOnce(new Error("timeout"))
      .mockResolvedValueOnce(created);
    const revokeProjectManagedSecret = vi.fn().mockRejectedValue(new Error("nope"));
    const client = clientWith({ createComposerSecret, revokeProjectManagedSecret });

    await expect(
      createComposerSecretForMark(client, "sess-1", "proj-1", request),
    ).rejects.toThrow(/still active/);
  });
});

describe("revokeComposerSecret", () => {
  it("revokes by the id inside the reference", async () => {
    const revokeProjectManagedSecret = vi.fn(async () => {});
    await revokeComposerSecret(
      clientWith({ revokeProjectManagedSecret }),
      "proj-1",
      REFERENCE,
    );
    expect(revokeProjectManagedSecret).toHaveBeenCalledWith("proj-1", SECRET_ID);
  });

  it("surfaces a failed revoke instead of swallowing it", async () => {
    const revokeProjectManagedSecret = vi.fn().mockRejectedValue(new Error("nope"));
    await expect(
      revokeComposerSecret(clientWith({ revokeProjectManagedSecret }), "proj-1", REFERENCE),
    ).rejects.toThrow(/still active/);
  });

  it("surfaces a reference it cannot revoke by", async () => {
    const revokeProjectManagedSecret = vi.fn(async () => {});
    await expect(
      revokeComposerSecret(clientWith({ revokeProjectManagedSecret }), "proj-1", "{{paintedwolf-secret:}}"),
    ).rejects.toThrow(/still active/);
    expect(revokeProjectManagedSecret).not.toHaveBeenCalled();
  });
});
