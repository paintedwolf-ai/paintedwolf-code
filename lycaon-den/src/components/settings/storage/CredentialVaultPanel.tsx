import { ErrorBoundary, Show, createResource } from "solid-js";
import { readVaultCustody } from "../../../platform/connection/vault-custody.ts";
import { CREDENTIAL_VAULT_COPY as C } from "../../../settings/storage/credential-vault-copy.ts";
import { settingAnchor } from "../../../settings/settings-registry.ts";

/** Reports the protected custodian that holds this device's credential key. */
export function CredentialVaultPanel() {
  const [custody] = createResource(readVaultCustody);
  return (
    <div class="den-settings-subsection" data-testid="credential-vault-panel" {...settingAnchor("credential-vault")}>
      <h3 class="den-settings-subhead">{C.heading}</h3>
      <ErrorBoundary fallback={<p class="den-settings-warn" role="alert">{C.loadFailed}</p>}>
        <Show when={custody()} keyed>
          {(report) => (
            <p class="den-settings-hint" data-testid="credential-vault-mode" data-mode={report.mode}>
              {C.modes[report.mode]}
            </p>
          )}
        </Show>
      </ErrorBoundary>
    </div>
  );
}
