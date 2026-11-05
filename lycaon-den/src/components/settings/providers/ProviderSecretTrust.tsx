import { Show, type Accessor } from "solid-js";
import type { ProviderMeta } from "../../../api/types.ts";
import { isIncludedProvider } from "../../../settings/providers/models-editor-model.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { secretScreenTrustEndpointNote } from "./provider-form-model.ts";

type Props = {
  provider: Accessor<ProviderMeta>;
  saving: Accessor<boolean>;
  onChange: (provider: ProviderMeta, trusted: boolean) => void;
};

export function ProviderSecretTrust(props: Props) {
  return <>
    <DenCheckbox
      data-testid={`provider-secret-trust-${props.provider().id}`}
      checked={props.provider().secret_screen_trusted ?? false}
      disabled={props.saving()}
      onChange={(event) => props.onChange(props.provider(), event.currentTarget.checked)}
    >
      {MODELS_SETTINGS_COPY.secretScreenTrustLabel}
    </DenCheckbox>
    <p class="den-settings-hint" data-testid={`provider-secret-trust-hint-${props.provider().id}`}>
      {isIncludedProvider(props.provider()) ? MODELS_SETTINGS_COPY.secretScreenTrustIncludedHint : MODELS_SETTINGS_COPY.secretScreenTrustHint}
      <Show when={!isIncludedProvider(props.provider()) && secretScreenTrustEndpointNote(props.provider())}>
        {(note) => <> {note()}</>}
      </Show>
    </p>
  </>;
}
