import type { VaultCustodyMode } from "../../platform/connection/vault-custody.ts";

export const CREDENTIAL_VAULT_COPY = {
  heading: "Credential vault",
  modes: {
    identity_service:
      "The Painted Wolf Code vault identity service holds the key to your saved credentials. The app opens them without a password, and only the installed app can reach the key.",
    keychain:
      "The macOS Keychain holds the key to your saved credentials, and only the signed app can read it.",
    development: "This development build keeps the vault key in a local file.",
    restart_required:
      "The installed package changed while this desktop was running. Quit Painted Wolf Code, finish any package operation, then open it from the installed desktop launcher. Your saved credentials are preserved.",
    installation_unverified:
      "Painted Wolf Code could not confirm which package owns this installation, so the vault status was not read. Wait for any package operation to finish, then reopen Settings. Your saved credentials are preserved.",
    unavailable: "The protected credential service is unavailable. Repair the installed service, then try again. Your saved credentials remain encrypted.",
    refused:
      "The vault identity service refused this copy of the app, so your saved credentials cannot be opened. Start Painted Wolf Code from the installed app.",
  } satisfies Record<VaultCustodyMode, string>,
  loadFailed: "The credential vault status could not be read.",
} as const;
