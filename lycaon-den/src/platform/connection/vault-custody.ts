import { invoke } from "@tauri-apps/api/core";

/** Who holds the credential vault's key, as the engine reports it. */
export type VaultCustodyMode =
  | "identity_service"
  | "app_password"
  | "keychain"
  | "development"
  | "refused";

export type VaultCustody = {
  mode: VaultCustodyMode;
  /** The identity service verified this app, so the key can move. */
  service_verified: boolean;
  detail?: string;
};

export type VaultCustodyTarget = "identity_service" | "app_password";

/** Mirrors `CustodyError` in `sidecar/custody.rs`; `code` is the engine's outcome code. */
export type VaultCustodyError = { code: string; message: string };

export function readVaultCustody(): Promise<VaultCustody> {
  return invoke<VaultCustody>("vault_custody");
}

/**
 * Stops the engine and moves the vault key. The caller restarts the engine
 * afterwards whether or not the move succeeded.
 */
export function moveVaultCustody(target: VaultCustodyTarget, password: string): Promise<void> {
  return invoke<void>("move_vault_custody", { target, password });
}

export function vaultCustodyErrorCode(err: unknown): string {
  if (err && typeof err === "object" && typeof (err as VaultCustodyError).code === "string") {
    return (err as VaultCustodyError).code;
  }
  return "failed";
}
