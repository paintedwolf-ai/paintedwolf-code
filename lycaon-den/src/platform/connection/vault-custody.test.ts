import { describe, expect, it, vi } from "vitest";

const invoke = vi.fn(async (..._args: unknown[]) => undefined as unknown);
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));

import {
  moveVaultCustody,
  readVaultCustody,
  vaultCustodyErrorCode,
  vaultCustodyStoppedEngine,
} from "./vault-custody.ts";

describe("vault custody commands", () => {
  it("calls the shell commands with the target and password", async () => {
    await readVaultCustody();
    await moveVaultCustody("app_password", "a long app password");
    expect(invoke).toHaveBeenNthCalledWith(1, "vault_custody");
    expect(invoke).toHaveBeenNthCalledWith(2, "move_vault_custody", {
      target: "app_password",
      password: "a long app password",
    });
  });

  it("reads the engine's outcome code from a shell error", () => {
    expect(vaultCustodyErrorCode({ code: "unchanged", message: "" })).toBe("unchanged");
    expect(vaultCustodyErrorCode(new Error("x"))).toBe("failed");
    expect(vaultCustodyErrorCode(null)).toBe("failed");
    expect(vaultCustodyStoppedEngine({ code: "password_incorrect", message: "", engine_stopped: false })).toBe(false);
    expect(vaultCustodyStoppedEngine({ code: "failed", message: "", engine_stopped: true })).toBe(true);
    expect(vaultCustodyStoppedEngine(new Error("join"))).toBe(true);
  });
});
