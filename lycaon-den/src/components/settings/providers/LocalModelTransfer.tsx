import { Show } from "solid-js";
import type { LocalGenerationStatus } from "../../../api/types.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { formatModelBytes } from "./model-size.ts";

type Props = {
  status: LocalGenerationStatus;
  busy: boolean;
  onCancel: () => void;
};

const phaseLabels = {
  preparing: "Preparing",
  downloading: "Downloading",
  importing: "Importing",
  activating: "Activating",
};

export function LocalModelTransfer(props: Props) {
  const name = () => props.status.available_models?.find(t => t.id === props.status.target_model)?.display_name ?? "local model";
  const phase = () => props.status.transfer_phase ?? "downloading";
  const received = () => Math.max(0, props.status.downloaded_bytes ?? 0);
  const total = () => props.status.total_bytes ?? 0;
  const movingBytes = () => phase() === "downloading" || phase() === "importing";
  const measured = () => movingBytes() && total() > 0;
  const percent = () => Math.floor(Math.min(received(), total()) / total() * 100);
  const byteLabel = () => total() > 0
    ? `${formatModelBytes(received())} of ${formatModelBytes(total())}`
    : `${formatModelBytes(received())} transferred`;

  return (
    <section class="local-model-transfer" aria-label="Model installation">
      <div class="flex items-baseline justify-between gap-3">
        <strong role="status">{phaseLabels[phase()]} {name()}…</strong>
        <Show when={measured()}><span class="tabular-nums">{percent()}%</span></Show>
      </div>
      <progress class="local-model-progress" aria-label={`${phaseLabels[phase()]} ${name()}`}
        value={measured() ? Math.min(received(), total()) : undefined} max={measured() ? total() : undefined} />
      <div class="flex flex-wrap items-center justify-between gap-2">
        <span class="den-settings-hint tabular-nums">
          {movingBytes() ? byteLabel() : phase() === "activating" ? "Starting the local runtime." : "Checking the model bundle."}
        </span>
        <Show when={phase() !== "activating"}>
          <DenButton variant="secondary" disabled={props.busy} onClick={props.onCancel}>Cancel transfer</DenButton>
        </Show>
      </div>
    </section>
  );
}
