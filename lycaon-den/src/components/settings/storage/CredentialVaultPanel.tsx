import { Show, createSignal, onMount } from "solid-js";
import { readVaultCustody, type VaultCustody } from "../../../platform/connection/vault-custody.ts";
import { CREDENTIAL_VAULT_COPY as C } from "../../../settings/storage/credential-vault-copy.ts";
import { settingAnchor } from "../../../settings/settings-registry.ts";

/** Reports the protected custodian that holds this device's credential key. */
export function CredentialVaultPanel() {
  const [report, setReport] = createSignal<VaultCustody>();
  const [failed, setFailed] = createSignal(false);
  onMount(() => {
    void readVaultCustody().then(setReport, () => setFailed(true));
  });
  return (
    <div class="den-settings-subsection" data-testid="credential-vault-panel" {...settingAnchor("credential-vault")}>
      <h3 class="den-settings-subhead">{C.heading}</h3>
      <Show when={!failed()} fallback={<p class="den-settings-warn" role="alert">{C.loadFailed}</p>}>
        <Show when={report()} keyed>
          {(report) => (
            <p class="den-settings-hint" data-testid="credential-vault-mode" data-mode={report.mode}>
              {C.modes[report.mode]}
            </p>
          )}
        </Show>
      </Show>
    </div>
  );
}
