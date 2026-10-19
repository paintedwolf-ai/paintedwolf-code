import type { VaultCustodyMode } from "../../platform/connection/vault-custody.ts";

export const CREDENTIAL_VAULT_COPY = {
  heading: "Credential vault",
  modes: {
    identity_service:
      "The Painted Wolf Code vault identity service holds the key to your saved credentials. The app opens them without a password, and only the installed app can reach the key.",
    app_password:
      "Your app password protects the key to your saved credentials. You enter it each time the app opens.",
    keychain:
      "The macOS Keychain holds the key to your saved credentials, and only the signed app can read it.",
    development: "This development build keeps the vault key in a local file.",
    restart_required:
      "The installed package changed while this desktop was running. Quit Painted Wolf Code, finish any package operation, then open it from the installed desktop launcher. Your saved credentials are preserved.",
    unavailable:
      "The vault custodian is unavailable. Your encrypted credentials are preserved. Restore the identity service and open the installed desktop app. An app password requires a previously exported key.",
    refused:
      "The vault identity service refused this copy of the app, so your saved credentials cannot be opened. Start Painted Wolf Code from the installed app.",
  } satisfies Record<VaultCustodyMode, string>,
  serviceAbsent:
    "The vault identity service is not running on this device, so the app password stays in use. Installing or enabling the service lets you move the key to it.",
  loadFailed: "The credential vault status could not be read.",
  toService: "Use the identity service…",
  toAppPassword: "Use an app password instead…",
  toServicePrompt:
    "Enter your app password to move the key into the vault identity service. The app restarts and then opens your saved credentials without asking for the password.",
  toAppPasswordPrompt:
    "Choose an app password. The key leaves the vault identity service, and you will enter this password each time the app opens. It cannot be recovered.",
  passwordLabel: "App password",
  newPasswordLabel: "New app password",
  confirmPasswordLabel: "Confirm app password",
  passwordHint: "Use at least 12 bytes.",
  passwordsDiffer: "The passwords do not match.",
  passwordTooShort: "Use an app password of at least 12 bytes.",
  confirmToService: "Move to the identity service",
  confirmToAppPassword: "Use this app password",
  cancel: "Cancel",
  moving: "Moving the key and restarting…",
  moved: "The vault key moved. The app restarted.",
  errors: {
    password_incorrect: "That app password did not open the vault. Nothing changed.",
    password_required: "Enter your app password.",
    password_invalid: "Use an app password of at least 12 bytes.",
    unchanged: "The key is already there.",
    desktop_restart_required:
      "The installed package changed. Quit Painted Wolf Code and reopen the installed desktop app. Your running engine and credentials are preserved.",
    service_unavailable:
      "The vault identity service is not available to this app. Nothing changed.",
    failed: "The vault key could not be moved.",
  } as Record<string, string>,
  restartFailed: "The app could not restart. Quit and reopen the app.",
} as const;

export const MIN_APP_PASSWORD_BYTES = 12;
