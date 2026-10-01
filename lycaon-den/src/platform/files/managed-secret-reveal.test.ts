import { beforeEach, describe, expect, it, vi } from "vitest";

const { invoke, isTauriRuntime } = vi.hoisted(() => ({
  invoke: vi.fn(),
  isTauriRuntime: vi.fn(() => true),
}));

vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime }));

import { revealManagedSecret } from "./managed-secret-reveal.ts";

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
      code: "managed_secret_reveal_unavailable",
    });
    expect(invoke).not.toHaveBeenCalled();
  });

  it("preserves structured native refusals", async () => {
    invoke.mockRejectedValue({
      code: "managed_secret_reveal_denied",
      message: "Authentication was canceled.",
      title: "Secret reveal was not authorized",
      suggested_action: "Choose Reveal again.",
      scope: "project",
    });
    await expect(revealManagedSecret("project-1", "secret-1")).rejects.toMatchObject({
      code: "managed_secret_reveal_denied",
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
