import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import { CredentialVaultPanel } from "./CredentialVaultPanel.tsx";
import type { VaultCustody, VaultCustodyMode } from "../../../platform/connection/vault-custody.ts";
const readVaultCustody = vi.fn<() => Promise<VaultCustody>>();
vi.mock("../../../platform/connection/vault-custody.ts", () => ({ readVaultCustody: () => readVaultCustody() }));
describe("CredentialVaultPanel", () => {
  beforeEach(() => readVaultCustody.mockReset());
  it.each(["keychain", "identity_service", "development", "unavailable", "refused"] satisfies VaultCustodyMode[])("reports %s without custody mutation controls", async (mode) => {
    readVaultCustody.mockResolvedValue({ mode, service_verified: mode === "identity_service" });
    const view = render(() => <CredentialVaultPanel />);
    expect((await view.findByTestId("credential-vault-mode")).getAttribute("data-mode")).toBe(mode);
    expect(view.container.querySelector("input, button, form")).toBeNull();
    expect(readVaultCustody).toHaveBeenCalledOnce();
  });
  it("retains a read failure without offering a reset", async () => {
    let rejectRead!: (error: Error) => void;
    readVaultCustody.mockReturnValue(new Promise<VaultCustody>((_resolve, reject) => {
      rejectRead = reject;
    }));
    const view = render(() => <CredentialVaultPanel />);
    rejectRead(new Error("unavailable"));
    expect(await view.findByRole("alert")).toBeTruthy();
    expect(view.container.querySelector("input, button, form")).toBeNull();
  });
});
