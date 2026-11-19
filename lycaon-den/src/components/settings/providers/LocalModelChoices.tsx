import { For, Show } from "solid-js";
import type { LocalModelTier } from "../../../api/types.ts";
import { DenRadio } from "../../primitives/DenRadio.tsx";
import { formatModelBytes } from "./model-size.ts";

type Props = {
  tiers: LocalModelTier[];
  selected: string;
  installed?: string;
  busy: boolean;
  onChoose: (id: string) => void;
};

export function LocalModelChoices(props: Props) {
  return (
    <fieldset class="m-0 grid gap-2 border-0 p-0" disabled={props.busy}>
      <legend class="sr-only">Local model for this device</legend>
      <For each={props.tiers}>{model => (
        <DenRadio class="local-model-option" name="local-model-tier" checked={props.selected === model.id}
          disabled={!model.eligible} onChange={() => props.onChoose(model.id)}>
          <span class="flex flex-wrap items-baseline gap-x-2">
            <strong>{model.display_name}</strong>
            <Show when={model.recommended}><span class="local-model-recommendation">Recommended for this device</span></Show>
          </span>
          <span class="mt-1 block">{model.description}</span>
          <small class="local-model-resources">
            <Show when={props.installed === model.id} fallback={
              <Show when={model.download_available && model.download_bytes > 0}
                fallback={<Show when={!model.download_available} fallback="Download available">Download unavailable</Show>}>
                {formatModelBytes(model.download_bytes)} download
              </Show>
            }>Installed</Show>
            <Show when={!model.eligible}> · Exceeds this device’s memory budget</Show>
          </small>
        </DenRadio>
      )}</For>
      <DenRadio class="local-model-option" name="local-model-tier" checked={!props.selected} onChange={() => props.onChoose("")}>
        Use no local model
      </DenRadio>
    </fieldset>
  );
}
