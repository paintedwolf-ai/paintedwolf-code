import { Show, type Accessor } from "solid-js";
import type { ProviderMeta } from "../../../api/types.ts";
import { localGenerationView } from "../../../settings/providers/local-generation-model.ts";
import { providerDisplayName } from "../../../settings/providers/models-editor-model.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { ProviderSecretTrust } from "./ProviderSecretTrust.tsx";

type Props = {
  provider: Accessor<ProviderMeta>;
  busy: Accessor<boolean>;
  failure: Accessor<string | undefined>;
  savingSecretTrust: Accessor<boolean>;
  onRecover: (provider: ProviderMeta, action: "retry" | "repair") => void;
  onSetSecretScreenTrust: (provider: ProviderMeta, trusted: boolean) => void;
};

export function LocalGenerationCard(props: Props) {
  const view = () => localGenerationView(props.provider());
  const status = () => props.provider().local_runtime;
  const detail = () => props.failure() || status()?.detail;
  const recover = () => {
    const action = view().action;
    if (action === "retry" || action === "repair") props.onRecover(props.provider(), action);
  };
  return (
    <article class="den-settings-provider-card" data-testid={`provider-card-${props.provider().id}`}>
      <header class="den-settings-provider-head">
        <div class="den-settings-provider-title">
          <span class="den-settings-provider-name" data-testid={`provider-name-${props.provider().id}`}>{providerDisplayName(props.provider())}</span>
          <span class="den-activity-span-count">{MODELS_SETTINGS_COPY.includedProviderBadge}</span>
        </div>
        <span class="den-settings-provider-status" data-configured={view().ready} role="status">
          {view().label}
        </span>
      </header>
      <div class="den-activity-span" data-testid={`provider-included-summary-${props.provider().id}`}>
        <div class="den-activity-span-chicklet">
          <span class="den-activity-span-chip">
            <span class="den-activity-span-name">{MODELS_SETTINGS_COPY.includedProviderModelSummary}</span>
          </span>
          <span class="den-activity-span-hint">{MODELS_SETTINGS_COPY.includedProviderHint}</span>
        </div>
      </div>
      <p class="den-settings-hint">{view().detail}</p>
      <Show when={view().ready && (status()?.context_tokens ?? 0) > 0}>
        <p class="den-settings-hint" data-testid="local-model-context">
          Context window: {status()!.context_tokens!.toLocaleString()} tokens
        </p>
      </Show>
      <Show when={view().reinstall}>
        <p class="den-settings-hint">Reinstall Painted Wolf Code to restore its bundled model files.</p>
      </Show>
      <Show when={view().action === "retry" || view().action === "repair"}>
        <div class="den-settings-provider-actions">
          <DenButton variant="secondary" disabled={props.busy() || view().busy} onClick={recover}>
            {props.busy() ? "Working…" : view().actionLabel}
          </DenButton>
        </div>
      </Show>
      <Show when={props.failure()}>
        <p class="den-settings-warn" role="alert">The last request did not finish successfully.</p>
      </Show>
      <Show when={detail() || status()?.revision}>
        <details class="den-settings-hint">
          <summary>Model details</summary>
          <Show when={status()?.revision}>{revision => <p>Version: {revision()}</p>}</Show>
          <Show when={detail()}>{message => <p>{message()}</p>}</Show>
        </details>
      </Show>
      <ProviderSecretTrust
        provider={props.provider}
        saving={props.savingSecretTrust}
        onChange={props.onSetSecretScreenTrust}
      />
    </article>
  );
}
