import { For, Show } from "solid-js";
import type { ProjectTrustSurface, TrustSurfaceId } from "../../api/types.ts";
import { TRUST_COPY, trustCountLabel, trustSurfaceHint } from "../../settings/security/trust-copy.ts";
import { instructionLineCount } from "../../settings/security/trust-model.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { SettingsListRow } from "../settings/SettingsListRow.tsx";

type Props = {
  surfaces: ProjectTrustSurface[];
  onToggle: (id: TrustSurfaceId, enabled: boolean) => void;
  onSelect: (surface: ProjectTrustSurface) => void;
  savingId: TrustSurfaceId | null;
  testIdPrefix: string;
};

export function TrustSurfaceRows(props: Props) {
  return (
    <div data-testid={`${props.testIdPrefix}-list`}>
      <For each={props.surfaces}>
        {(surface) => {
          const off = !surface.applying;
          return (
            <SettingsListRow
              testId={`${props.testIdPrefix}-row-${surface.id}`}
              leading={
                <DenCheckbox
                  checked={surface.project_enabled}
                  disabled={!surface.device_enabled || props.savingId === surface.id}
                  data-testid={`${props.testIdPrefix}-toggle-${surface.id}`}
                  onChange={(event) =>
                    props.onToggle(surface.id, event.currentTarget.checked)
                  }
                >
                  <span class="sr-only">{surface.label}</span>
                </DenCheckbox>
              }
              primary={surface.label}
              secondary={trustSurfaceHint(surface.id)}
              status={
                <span>
                  <Show when={off}>
                    <span class="trust-surface-row__state" data-state="off">
                      {surface.device_enabled ? TRUST_COPY.offRow : TRUST_COPY.deviceOffRow}
                      {" · "}
                    </span>
                  </Show>
                  <span
                    class="trust-surface-row__count"
                    data-testid={`${props.testIdPrefix}-count-${surface.id}`}
                  >
                    {countText(surface)}
                  </span>
                </span>
              }
              onSelect={() => props.onSelect(surface)}
            />
          );
        }}
      </For>
    </div>
  );
}

function countText(surface: ProjectTrustSurface): string {
  const base = trustCountLabel(surface.id, surface.count);
  if (surface.id !== "agents_md") return base;
  const lines = instructionLineCount(surface);
  return lines > 0 ? `${base}, ${lines} lines` : base;
}
