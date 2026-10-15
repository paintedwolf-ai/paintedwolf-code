import { Show, createResource, createSignal } from "solid-js";
import {
  moveVaultCustody,
  readVaultCustody,
  vaultCustodyErrorCode,
  vaultCustodyStoppedEngine,
  type VaultCustody,
  type VaultCustodyTarget,
} from "../../../platform/connection/vault-custody.ts";
import { restartAppBackend } from "../../../platform/connection/app-connection.ts";
import { tauriPlatform } from "../../../platform/runtime.ts";
import {
  CREDENTIAL_VAULT_COPY as C,
  MIN_APP_PASSWORD_BYTES,
} from "../../../settings/storage/credential-vault-copy.ts";
import { settingAnchor } from "../../../settings/settings-registry.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";

/** The move a custody report offers, if any. */
export function offeredMove(custody: VaultCustody): VaultCustodyTarget | null {
  if (!custody.service_verified) return null;
  if (custody.mode === "app_password") return "identity_service";
  if (custody.mode === "identity_service") return "app_password";
  return null;
}

/** Shows who holds the vault key and moves it with a typed password. */
export function CredentialVaultPanel() {
  const [custody, { refetch }] = createResource(readVaultCustody);
  const [target, setTarget] = createSignal<VaultCustodyTarget | null>(null);
  const [password, setPassword] = createSignal("");
  const [confirmation, setConfirmation] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<string>();
  const [status, setStatus] = createSignal<string>();

  const close = () => {
    setTarget(null);
    setPassword("");
    setConfirmation("");
  };

  const invalid = (to: VaultCustodyTarget): string | undefined => {
    if (to !== "app_password") return password() === "" ? C.errors.password_required : undefined;
    if (new TextEncoder().encode(password()).byteLength < MIN_APP_PASSWORD_BYTES) return C.passwordTooShort;
    if (password() !== confirmation()) return C.passwordsDiffer;
    return undefined;
  };

  const submit = async (to: VaultCustodyTarget) => {
    const problem = invalid(to);
    setStatus(undefined);
    setError(problem);
    if (problem) return;
    setBusy(true);
    let moved = false;
    try {
      await moveVaultCustody(to, password());
      moved = true;
    } catch (err) {
      setError(C.errors[vaultCustodyErrorCode(err)] ?? C.errors.failed);
      if (!vaultCustodyStoppedEngine(err)) {
        setBusy(false);
        return;
      }
    }
    try {
      await restartAppBackend();
      if (moved) {
        close();
        setStatus(C.moved);
      }
    } catch {
      setError(C.restartFailed);
    } finally {
      setBusy(false);
      void refetch();
    }
  };

  return (
    <div class="den-settings-subsection" data-testid="credential-vault-panel" {...settingAnchor("credential-vault")}>
      <h3 class="den-settings-subhead">{C.heading}</h3>
      <Show when={!custody.error} fallback={<p class="den-settings-warn" role="alert">{C.loadFailed}</p>}>
        <Show when={custody()} keyed>
          {(report) => (
            <>
              <p class="den-settings-hint" data-testid="credential-vault-mode" data-mode={report.mode}>
                {C.modes[report.mode]}
              </p>
              <Show when={report.mode === "app_password" && !report.service_verified && tauriPlatform() === "linux"}>
                <p class="den-settings-hint" data-testid="credential-vault-service-absent">{C.serviceAbsent}</p>
              </Show>
              <Show when={offeredMove(report)} keyed>
                {(to) => (
                  <Show
                    when={target() === to}
                    fallback={
                      <div class="den-settings-actions">
                        <DenButton variant="secondary" data-testid="credential-vault-move" onClick={() => { setError(undefined); setTarget(to); }}>
                          {to === "identity_service" ? C.toService : C.toAppPassword}
                        </DenButton>
                      </div>
                    }
                  >
                    <form
                      data-testid="credential-vault-move-form"
                      onSubmit={(event) => {
                        event.preventDefault();
                        if (!busy()) void submit(to);
                      }}
                    >
                      <p class="den-settings-hint">{to === "identity_service" ? C.toServicePrompt : C.toAppPasswordPrompt}</p>
                      <DenField
                        label={to === "identity_service" ? C.passwordLabel : C.newPasswordLabel}
                        hint={to === "app_password" ? C.passwordHint : undefined}
                      >
                        <DenInput
                          type="password"
                          autocomplete={to === "identity_service" ? "current-password" : "new-password"}
                          data-testid="credential-vault-password"
                          value={password()}
                          disabled={busy()}
                          onInput={(event) => setPassword(event.currentTarget.value)}
                        />
                      </DenField>
                      <Show when={to === "app_password"}>
                        <DenField label={C.confirmPasswordLabel}>
                          <DenInput
                            type="password"
                            autocomplete="new-password"
                            data-testid="credential-vault-password-confirmation"
                            value={confirmation()}
                            disabled={busy()}
                            onInput={(event) => setConfirmation(event.currentTarget.value)}
                          />
                        </DenField>
                      </Show>
                      <div class="den-settings-actions">
                        <DenButton type="submit" variant="primary" data-testid="credential-vault-confirm" disabled={busy()}>
                          {busy() ? C.moving : to === "identity_service" ? C.confirmToService : C.confirmToAppPassword}
                        </DenButton>
                        <DenButton type="button" variant="secondary" disabled={busy()} onClick={close}>
                          {C.cancel}
                        </DenButton>
                      </div>
                    </form>
                  </Show>
                )}
              </Show>
            </>
          )}
        </Show>
      </Show>
      <Show when={status()} keyed>
        {(text) => <p class="den-settings-hint" role="status" data-testid="credential-vault-status">{text}</p>}
      </Show>
      <Show when={error()} keyed>
        {(text) => <p class="den-settings-warn" role="alert" data-testid="credential-vault-error">{text}</p>}
      </Show>
    </div>
  );
}
