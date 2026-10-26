import { invoke } from "@tauri-apps/api/core";

/** Who holds the credential vault's key, as the engine reports it. */
export type VaultCustodyMode =
  | "identity_service"
  | "keychain"
  | "development"
  // Shell-only state when the installed package no longer matches this process.
  | "restart_required"
  | "installation_unverified"
  | "unavailable"
  | "refused";

export type VaultCustody = {
  mode: VaultCustodyMode;
  /** The identity service authenticated this engine instance. */
  service_verified: boolean;
  detail?: string;
};

/** Reads the protected key custodian without changing it. */
export function readVaultCustody(): Promise<VaultCustody> {
  return invoke<VaultCustody>("vault_custody");
}
