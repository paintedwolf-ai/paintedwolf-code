import { beforeEach, describe, expect, it, vi } from "vitest";

const { invoke, isTauriRuntime } = vi.hoisted(() => ({
  invoke: vi.fn(),
  isTauriRuntime: vi.fn(() => true),
}));

vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("./runtime.ts", () => ({ isTauriRuntime }));

import { resolveCheckpointWithPresence, revealManagedSecret } from "./presence.ts";

describe("revealManagedSecret", () => {
  beforeEach(() => {
    invoke.mockReset();
    isTauriRuntime.mockReturnValue(true);
  });

  it("uses only the native reveal command", async () => {
    invoke.mockResolvedValue({
      secret_value: "credential",
      version: 2,
      revealed_at: "2026-09-01T12:00:00Z",
      remask_after_ms: 30000,
    });
    await expect(revealManagedSecret("project-1", "secret-1")).resolves.toMatchObject({
      secret_value: "credential",
      version: 2,
    });
    expect(invoke).toHaveBeenCalledWith("reveal_managed_secret", {
      projectId: "project-1",
      secretId: "secret-1",
    });
  });

  it("is unavailable outside the installed app", async () => {
    isTauriRuntime.mockReturnValue(false);
    await expect(revealManagedSecret("project-1", "secret-1")).rejects.toMatchObject({
      code: "presence_unavailable",
    });
    expect(invoke).not.toHaveBeenCalled();
  });

  it("preserves structured native refusals", async () => {
    invoke.mockRejectedValue({
      code: "presence_denied",
      message: "Authentication was canceled.",
      title: "Secret reveal was not authorized",
      suggested_action: "Choose Reveal again.",
      scope: "project",
    });
    await expect(revealManagedSecret("project-1", "secret-1")).rejects.toMatchObject({
      code: "presence_denied",
      message: "Authentication was canceled.",
      title: "Secret reveal was not authorized",
      suggestedAction: "Choose Reveal again.",
      scope: "project",
    });
  });

  it("carries the host's notice actions from the native refusal", async () => {
    invoke.mockRejectedValue({
      code: "no_model",
      message: "No model is configured.",
      actions: ["open_ai_providers", "prompt_retry"],
    });
    await expect(revealManagedSecret("project-1", "secret-1")).rejects.toMatchObject({
      actions: ["open_ai_providers", "prompt_retry"],
    });
  });
});

describe("resolveCheckpointWithPresence", () => {
  beforeEach(() => {
    invoke.mockReset();
    isTauriRuntime.mockReturnValue(true);
  });

  it("lets the native shell resolve the checkpoint", async () => {
    invoke.mockResolvedValue({ id: "checkpoint-1", status: "approved" });
    await expect(resolveCheckpointWithPresence("session-1", "checkpoint-1", "lease-chat")).resolves.toMatchObject({
      status: "approved",
    });
    expect(invoke).toHaveBeenCalledWith("resolve_checkpoint_with_presence", {
      sessionId: "session-1",
      checkpointId: "checkpoint-1",
      optionId: "lease-chat",
    });
  });

  it("is unavailable outside the installed app", async () => {
    isTauriRuntime.mockReturnValue(false);
    await expect(resolveCheckpointWithPresence("session-1", "checkpoint-1", "once")).rejects.toMatchObject({
      code: "presence_unavailable",
    });
    expect(invoke).not.toHaveBeenCalled();
  });
});
