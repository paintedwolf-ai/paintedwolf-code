import { For, Show } from "solid-js";
import type { LocalGenerationStatus, LocalModelTier } from "../../../api/types.ts";
import { DenRadioControl } from "../../primitives/DenRadio.tsx";
import { formatModelBytes } from "./model-size.ts";

type Props = {
  tiers: LocalModelTier[];
  selected: string;
  installed?: string;
  memoryKind?: LocalGenerationStatus["memory_kind"];
  busy: boolean;
  onChoose: (id: string) => void;
};

const memoryLabels = {
  system: "system memory",
  gpu: "GPU memory",
} satisfies Record<NonNullable<LocalGenerationStatus["memory_kind"]>, string>;

export function LocalModelChoices(props: Props) {
  return (
    <fieldset class="m-0 grid gap-2 border-0 p-0" disabled={props.busy}>
      <legend class="sr-only">Local model for this device</legend>
      <For each={props.tiers}>{model => (
        <label class="local-model-option">
          <DenRadioControl name="local-model-tier" checked={props.selected === model.id}
            disabled={!model.eligible} onChange={() => props.onChoose(model.id)} />
          <span class="min-w-0">
            <span class="local-model-title flex flex-wrap items-baseline gap-x-2">
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
              <Show when={props.memoryKind} keyed>{kind =>
                <> · Requires {formatModelBytes(model.min_memory_bytes, "up")} of {memoryLabels[kind]}</>
              }</Show>
              <Show when={!model.eligible}> · Exceeds this device’s memory budget</Show>
            </small>
          </span>
        </label>
      )}</For>
      <label class="local-model-option">
        <DenRadioControl name="local-model-tier" checked={!props.selected} onChange={() => props.onChoose("")} />
        <span class="local-model-title">Use no local model</span>
      </label>
    </fieldset>
  );
}
