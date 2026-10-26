import { describe, expect, it, vi } from "vitest";
const invoke = vi.fn(async (..._args: unknown[]) => undefined as unknown);
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));
import { readVaultCustody } from "./vault-custody.ts";
describe("vault custody observation", () => {
  it("reads custody without a credential or mutation argument", async () => {
    const report = { mode: "identity_service", service_verified: true };
    invoke.mockResolvedValueOnce(report);
    expect(await readVaultCustody()).toEqual(report);
    expect(invoke).toHaveBeenCalledExactlyOnceWith("vault_custody");
  });
});
