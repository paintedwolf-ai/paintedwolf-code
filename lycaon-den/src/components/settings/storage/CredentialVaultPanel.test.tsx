import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { CredentialVaultPanel, offeredMove } from "./CredentialVaultPanel.tsx";
import { CREDENTIAL_VAULT_COPY as C } from "../../../settings/storage/credential-vault-copy.ts";
import type { VaultCustody } from "../../../platform/connection/vault-custody.ts";

const readVaultCustody = vi.fn<() => Promise<VaultCustody>>();
const moveVaultCustody = vi.fn<(target: string, password: string) => Promise<void>>();
const restartAppBackend = vi.fn(async () => undefined);
let platform: "linux" | "macos" = "linux";

vi.mock("../../../platform/connection/vault-custody.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../platform/connection/vault-custody.ts")>()),
  readVaultCustody: () => readVaultCustody(),
  moveVaultCustody: (target: string, password: string) => moveVaultCustody(target, password),
}));
vi.mock("../../../platform/connection/app-connection.ts", () => ({
  restartAppBackend: () => restartAppBackend(),
}));
vi.mock("../../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../platform/runtime.ts")>()),
  tauriPlatform: () => platform,
}));

const input = (el: HTMLElement, value: string) => fireEvent.input(el, { target: { value } });

describe("CredentialVaultPanel", () => {
  beforeEach(() => {
    platform = "linux";
    readVaultCustody.mockReset();
    moveVaultCustody.mockReset().mockResolvedValue(undefined);
    restartAppBackend.mockReset().mockResolvedValue(undefined);
  });

  it("offers a move only where the identity service verified the app", () => {
    expect(offeredMove({ mode: "app_password", service_verified: true })).toBe("identity_service");
    expect(offeredMove({ mode: "identity_service", service_verified: true })).toBe("app_password");
    expect(offeredMove({ mode: "app_password", service_verified: false })).toBeNull();
    expect(offeredMove({ mode: "refused", service_verified: true })).toBeNull();
  });

  it("names the app password path and the absent service on Linux", async () => {
    readVaultCustody.mockResolvedValue({ mode: "app_password", service_verified: false });
    const { findByTestId, queryByTestId } = render(() => <CredentialVaultPanel />);
    expect((await findByTestId("credential-vault-mode")).textContent).toBe(C.modes.app_password);
    expect(queryByTestId("credential-vault-service-absent")).toBeTruthy();
    expect(queryByTestId("credential-vault-move")).toBeNull();
  });

  it("does not mention the identity service on other platforms", async () => {
    platform = "macos";
    readVaultCustody.mockResolvedValue({ mode: "app_password", service_verified: false });
    const { findByTestId, queryByTestId } = render(() => <CredentialVaultPanel />);
    await findByTestId("credential-vault-mode");
    expect(queryByTestId("credential-vault-service-absent")).toBeNull();
  });

  it("moves into the service with the typed app password and restarts", async () => {
    readVaultCustody
      .mockResolvedValueOnce({ mode: "app_password", service_verified: true })
      .mockResolvedValue({ mode: "identity_service", service_verified: true });
    const { findByTestId, getByTestId } = render(() => <CredentialVaultPanel />);
    fireEvent.click(await findByTestId("credential-vault-move"));
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    expect(getByTestId("credential-vault-error").textContent).toBe(C.errors.password_required);
    expect(moveVaultCustody).not.toHaveBeenCalled();

    input(getByTestId("credential-vault-password"), "correct horse battery");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    await waitFor(() => expect(getByTestId("credential-vault-status").textContent).toBe(C.moved));
    expect(moveVaultCustody).toHaveBeenCalledWith("identity_service", "correct horse battery");
    expect(restartAppBackend).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(getByTestId("credential-vault-mode").getAttribute("data-mode")).toBe("identity_service"),
    );
  });

  it("checks a new app password before moving the key out of the service", async () => {
    readVaultCustody.mockResolvedValue({ mode: "identity_service", service_verified: true });
    const { findByTestId, getByTestId } = render(() => <CredentialVaultPanel />);
    fireEvent.click(await findByTestId("credential-vault-move"));
    input(getByTestId("credential-vault-password"), "short");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    expect(getByTestId("credential-vault-error").textContent).toBe(C.passwordTooShort);

    input(getByTestId("credential-vault-password"), "a long app password");
    input(getByTestId("credential-vault-password-confirmation"), "a different password");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    expect(getByTestId("credential-vault-error").textContent).toBe(C.passwordsDiffer);

    input(getByTestId("credential-vault-password-confirmation"), "a long app password");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    await waitFor(() => expect(moveVaultCustody).toHaveBeenCalledWith("app_password", "a long app password"));
  });

  it("keeps the engine running when the shell refuses the password before stopping it", async () => {
    readVaultCustody.mockResolvedValue({ mode: "app_password", service_verified: true });
    moveVaultCustody.mockRejectedValue({ code: "password_incorrect", message: "wrong", engine_stopped: false });
    const { findByTestId, getByTestId } = render(() => <CredentialVaultPanel />);
    fireEvent.click(await findByTestId("credential-vault-move"));
    input(getByTestId("credential-vault-password"), "not the password");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    await waitFor(() => expect(getByTestId("credential-vault-error").textContent).toBe(C.errors.password_incorrect));
    expect(restartAppBackend).not.toHaveBeenCalled();
    expect((getByTestId("credential-vault-confirm") as HTMLButtonElement).disabled).toBe(false);
  });

  it("reports the engine's refusal and still restarts the engine", async () => {
    readVaultCustody.mockResolvedValue({ mode: "app_password", service_verified: true });
    moveVaultCustody.mockRejectedValue({ code: "password_incorrect", message: "wrong", engine_stopped: true });
    const { findByTestId, getByTestId, queryByTestId } = render(() => <CredentialVaultPanel />);
    fireEvent.click(await findByTestId("credential-vault-move"));
    input(getByTestId("credential-vault-password"), "not the password");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    await waitFor(() => expect(getByTestId("credential-vault-error").textContent).toBe(C.errors.password_incorrect));
    expect(restartAppBackend).toHaveBeenCalledTimes(1);
    expect(queryByTestId("credential-vault-status")).toBeNull();
    expect(getByTestId("credential-vault-move-form")).toBeTruthy();
  });

  it("tells the person to reopen the app when the engine does not restart", async () => {
    readVaultCustody.mockResolvedValue({ mode: "app_password", service_verified: true });
    restartAppBackend.mockRejectedValue(new Error("down"));
    const { findByTestId, getByTestId } = render(() => <CredentialVaultPanel />);
    fireEvent.click(await findByTestId("credential-vault-move"));
    input(getByTestId("credential-vault-password"), "correct horse battery");
    fireEvent.submit(getByTestId("credential-vault-move-form"));
    await waitFor(() => expect(getByTestId("credential-vault-error").textContent).toBe(C.restartFailed));
  });

  it("says when the status cannot be read", async () => {
    readVaultCustody.mockRejectedValue(new Error("no engine binary"));
    const { findByRole } = render(() => <CredentialVaultPanel />);
    expect((await findByRole("alert")).textContent).toBe(C.loadFailed);
  });
});
