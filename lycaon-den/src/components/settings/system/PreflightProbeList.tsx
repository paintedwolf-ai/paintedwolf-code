import { preflightReport } from "../../../platform/persistence/preflight-report.ts";
import {
  preflightRefreshing,
  refreshPreflight,
} from "../../../platform/persistence/preflight-store.ts";
import { For, Show } from "solid-js";
import type { JSX } from "solid-js";
import type { PreflightProbe } from "../../../api/types.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";

function probeTone(status: PreflightProbe["status"]): "positive" | "warning" | "danger" {
  switch (status) {
    case "ok":
      return "positive";
    case "degraded":
      return "warning";
    case "blocked":
      return "danger";
  }
}

function probeVariant(status: PreflightProbe["status"]): "default" | "warning" | "rejected" {
  switch (status) {
    case "ok":
      return "default";
    case "degraded":
      return "warning";
    case "blocked":
      return "rejected";
  }
}

/** Environment readiness under Advanced → Diagnostics. Missing catalog copy shows the probe id. */
export function PreflightProbeList(): JSX.Element {
  // Store swallows sidecar failure; Re-check refreshes the same store the boot gate reads.
  const report = preflightReport;
  const recheck = () => void refreshPreflight();

  return (
    <section class="den-settings-subsection" data-testid="preflight-probe-list">
      <h3 class="den-settings-subhead">Environment</h3>

      <Show
        when={report()}
        fallback={
          <p class="den-settings-hint" data-testid="preflight-probes-unavailable">
            {preflightRefreshing() ? "Checking…" : "Readiness checks are unavailable."}
          </p>
        }
      >
        {(loaded) => (
          <>
            <p class="den-settings-hint" data-testid="preflight-overall">
              Overall: {loaded().overall}
            </p>
            <div class="den-settings-list">
              <For each={loaded().probes}>
                {(probe) => (
                  <SettingsListRow
                    testId={`preflight-probe-${probe.id}`}
                    variant={probeVariant(probe.status)}
                    primary={probe.title ?? probe.id}
                    secondary={
                      <Show when={probe.message || probe.suggested_action}>
                        <Show when={probe.message}>{(message) => <span>{message()}</span>}</Show>
                        <Show when={probe.message && probe.suggested_action}> </Show>
                        <Show when={probe.suggested_action}>{(action) => <span>{action()}</span>}</Show>
                      </Show>
                    }
                    status={
                      <span
                        class="den-status-mark"
                        data-tone={probeTone(probe.status)}
                        data-testid={`preflight-status-${probe.id}`}
                      >
                        {probe.status}
                      </span>
                    }
                  />
                )}
              </For>
            </div>
          </>
        )}
      </Show>

      <DenButton
        variant="secondary"
        data-testid="preflight-recheck"
        disabled={preflightRefreshing()}
        onClick={() => void recheck()}
      >
        Re-check
      </DenButton>
    </section>
  );
}
