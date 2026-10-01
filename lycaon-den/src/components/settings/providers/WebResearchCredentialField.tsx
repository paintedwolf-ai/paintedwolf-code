import { Show } from "solid-js";
import type { WebResearchProviderMeta } from "../../../api/types.ts";
import { WEB_RESEARCH_SETTINGS_COPY } from "../../../settings/research/web-research-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";

export type WebResearchCredentialDraft = {
  apiKey: string;
  savingKey: boolean;
};

type Props = {
  meta: WebResearchProviderMeta;
  draft: WebResearchCredentialDraft;
  onPatch: (patch: Partial<WebResearchCredentialDraft>) => void;
  onSave: () => void;
  onClear: () => void;
};

export function WebResearchCredentialField(props: Props) {
  const required = () =>
    props.meta.kind === "keyed" || props.meta.kind === "keyed_extra";
  const credentialStatus = () => {
    if (props.meta.credential_present) {
      return WEB_RESEARCH_SETTINGS_COPY.apiKeyStoredStatus;
    }
    if (props.meta.credential_source === "environment") {
      return WEB_RESEARCH_SETTINGS_COPY.apiKeyEnvironmentStatus;
    }
    return required()
      ? WEB_RESEARCH_SETTINGS_COPY.apiKeyMissingStatus
      : WEB_RESEARCH_SETTINGS_COPY.apiKeyOptionalStatus;
  };
  return (
    <Show when={props.meta.credential_slot}>
      <Show when={props.meta.credential_source === "environment"}>
        <p class="den-settings-hint">{WEB_RESEARCH_SETTINGS_COPY.envKeyHint}</p>
      </Show>
      <DenField
        label={
          <span class="den-settings-provider-key-label">
            <span>API key</span>
            <span
              class="den-settings-provider-status"
              classList={{
                "den-settings-warn":
                  required() && props.meta.credential_source === "none",
              }}
              data-configured={
                !required() || props.meta.credential_source !== "none"
              }
              data-testid={`web-research-key-status-${props.meta.id}`}
            >
              {credentialStatus()}
            </span>
          </span>
        }
      >
        <DenInput
          type="password"
          autocomplete="off"
          class="den-settings-api-key-input"
          data-testid={`web-research-key-${props.meta.id}`}
          data-credential={props.meta.credential_present ? "stored" : "missing"}
          data-required={required() ? "true" : "false"}
          placeholder={
            props.meta.credential_present
              ? WEB_RESEARCH_SETTINGS_COPY.apiKeyPlaceholderStored
              : WEB_RESEARCH_SETTINGS_COPY.apiKeyPlaceholder
          }
          value={props.draft.apiKey}
          onInput={(e) => props.onPatch({ apiKey: e.currentTarget.value })}
        />
      </DenField>
      <div class="den-settings-provider-actions">
        <DenButton
          variant="secondary"
          data-testid={`web-research-save-key-${props.meta.id}`}
          disabled={props.draft.savingKey || !props.draft.apiKey.trim()}
          onClick={() => props.onSave()}
        >
          {WEB_RESEARCH_SETTINGS_COPY.save}
        </DenButton>
        <Show when={props.meta.credential_present}>
          <DenButton
            variant="secondary"
            data-testid={`web-research-clear-key-${props.meta.id}`}
            disabled={props.draft.savingKey}
            onClick={() => props.onClear()}
          >
            {WEB_RESEARCH_SETTINGS_COPY.clear}
          </DenButton>
        </Show>
      </div>
    </Show>
  );
}
