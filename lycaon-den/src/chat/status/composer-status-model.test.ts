import { describe, expect, it } from "vitest";
import type { ModelPolicy, Session } from "../../api/types.ts";
import {
  coordinatorModelStatus,
  modelChipLabel,
  modelRefLabel,
} from "./composer-status-model.ts";

const policy: ModelPolicy = {
  coordinator: { provider_id: "fireworks", model: "kimi-k2" },
  lite: { provider_id: "fireworks", model: "small-1" },
  agent_pool: { selection: "round_robin", models: [] },
};

function session(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "p1",
    posture: "build",
    status: "idle",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  } as Session;
}

describe("coordinatorModelStatus", () => {
  it("prefers the session override over the effective policy", () => {
    const status = coordinatorModelStatus(
      session({ provider_id: "ollama", model: "local-9b" }),
      policy,
    );
    expect(status).toEqual({
      ref: { provider_id: "ollama", model: "local-9b" },
      sessionOverride: true,
    });
  });

  it("falls back to the effective coordinator slot", () => {
    const status = coordinatorModelStatus(session(), policy);
    expect(status).toEqual({
      ref: policy.coordinator,
      sessionOverride: false,
    });
  });

  it("ignores a half-set override (provider without model)", () => {
    const status = coordinatorModelStatus(
      session({ provider_id: "ollama" }),
      policy,
    );
    expect(status?.ref).toEqual(policy.coordinator);
  });

  it("is null before the policy loads and with nothing assigned", () => {
    expect(coordinatorModelStatus(session(), null)).toBeNull();
    expect(
      coordinatorModelStatus(session(), {
        ...policy,
        coordinator: { provider_id: "", model: "" },
      }),
    ).toBeNull();
  });
});

describe("chip labels", () => {
  it("chip shows the short model name; tooltip form shows provider / short name", () => {
    const status = coordinatorModelStatus(session(), policy)!;
    expect(modelChipLabel(status)).toBe("kimi-k2");
    expect(modelRefLabel(status.ref)).toBe("fireworks / kimi-k2");
  });

  it("strips provider path prefixes from the chip label", () => {
    const status = coordinatorModelStatus(
      session(),
      {
        ...policy,
        coordinator: {
          provider_id: "fireworks",
          model: "accounts/fireworks/models/kimi-k2p7-code",
        },
      },
    )!;
    expect(modelChipLabel(status)).toBe("kimi-k2p7-code");
    expect(modelRefLabel(status.ref)).toBe("fireworks / kimi-k2p7-code");
  });

  it("unset ref renders an em dash", () => {
    expect(modelRefLabel({ provider_id: "", model: "" })).toBe("—");
  });
});
