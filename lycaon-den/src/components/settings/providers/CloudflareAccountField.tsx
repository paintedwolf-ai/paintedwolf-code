import { Show } from "solid-js";
import { cloudflareAccountEndpoint } from "../../../settings/providers/cloudflare-endpoint.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";

export function CloudflareAccountField(props: {
  baseUrl: string;
  testId: string;
  onInput: (baseUrl: string) => void;
}) {
  const endpoint = () => cloudflareAccountEndpoint(props.baseUrl);
  return (
    <Show when={endpoint() !== null}>
      <DenField
        label={MODELS_SETTINGS_COPY.cloudflareAccountIdLabel}
        hint={MODELS_SETTINGS_COPY.cloudflareAccountIdHint}
        hintId={`${props.testId}-hint`}
      >
        <DenInput
          type="text"
          autocomplete="off"
          spellcheck={false}
          required
          data-testid={props.testId}
          aria-label={MODELS_SETTINGS_COPY.cloudflareAccountIdLabel}
          aria-describedby={`${props.testId}-hint`}
          value={endpoint()?.accountId ?? ""}
          onInput={(event) => {
            const next = endpoint()?.withAccountId(event.currentTarget.value);
            if (next !== undefined) props.onInput(next);
          }}
        />
      </DenField>
    </Show>
  );
}
