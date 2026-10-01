import { preflightReport } from "../../platform/persistence/preflight-report.ts";
import { createSignal, Show } from "solid-js";
import type { JSX } from "solid-js";
import type { PreflightProbe, PreflightReport } from "../../api/types.ts";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { SystemNudge } from "../SystemNudge.tsx";
import { dismissPreflight, isPreflightDismissed } from "./preflight-dismissals.ts";
import { noticeAction } from "../../notices/notice-actions.ts";

type Props = {
  /** Codes another card on this screen already states (`docs/den.md` § In-app notification stack). */
  statedElsewhere?: readonly string[];
  /** Opens Settings → Advanced → Diagnostics, where every probe is listed. */
  onOpenDiagnostics?: () => void;
};

/**
 * The first non-ok, non-catastrophic, non-project probe not stated elsewhere.
 * Catastrophic results belong to CriticalStop; project-scoped ones render in that
 * project's docks. A stated code yields to the next candidate.
 */
function firstNonCatastrophicProbe(
  report: PreflightReport | undefined,
  stated: readonly string[],
): PreflightProbe | undefined {
  return report?.probes.find(
    (probe) =>
      probe.status !== "ok" &&
      probe.tier === "non_catastrophic" &&
      probe.scope !== "project" &&
      !stated.includes(probe.code ?? ""),
  );
}

/** Home readiness card for non-catastrophic probes; copy and tier come from the wire. */
export function PreflightNudge(props: Props): JSX.Element {
  const [dismissedTick, setDismissedTick] = createSignal(0);

  // Shared readiness store; a failed read leaves the report undefined and the card hidden.
  const report = preflightReport;

  const visible = (): PreflightProbe | undefined => {
    const probe = firstNonCatastrophicProbe(
      report(),
      props.statedElsewhere ?? [],
    );
    if (!probe) return undefined;
    // Read the signal so a dismissal re-evaluates.
    dismissedTick();
    return isPreflightDismissed(probe.id, probe.code ?? "") ? undefined : probe;
  };

  return (
    // Each refresh deserializes a fresh probe; the id decides identity.
    <ShowLatest when={visible()} by={(probe) => probe.id}>
      {(probe) => {
        const action = () => noticeAction({ actions: probe().actions });
        const primaryFromAction = () => {
          const a = action();
          return a ? { label: a.label, onClick: () => a.run() } : undefined;
        };
        return (
          <SystemNudge
            testId="preflight-nudge"
            title={probe().title ?? probe().id}
            description={
              <>
                {probe().message}
                <Show when={probe().suggested_action} keyed>
                  {(suggestion) => (
                    <span class="system-nudge__hint">{suggestion}</span>
                  )}
                </Show>
              </>
            }
            tertiaryAction={
              props.onOpenDiagnostics
                ? { label: "Open diagnostics", onClick: () => props.onOpenDiagnostics?.() }
                : undefined
            }
            primaryAction={primaryFromAction()}
            onDismiss={() => {
              dismissPreflight(probe().id, probe().code ?? "");
              setDismissedTick((n) => n + 1);
            }}
          />
        );
      }}
    </ShowLatest>
  );
}
