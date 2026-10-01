import { Show } from "solid-js";

/** Circumference of the context ring (2πr, r = 7 in the 18×18 viewBox). */
const CONTEXT_RING_CIRCUMFERENCE = 2 * Math.PI * 7;

export type ContextRingProps = {
  /** Prompt tokens on the latest turn — current window occupancy. */
  prompt?: number;
  /** Model's full context window in tokens; the ring's denominator. */
  window?: number;
  /** Prompt-token level at which compaction fires; tips the ring into "warn". */
  compactionThreshold?: number;
  /** Smaller variant for dense surfaces like worker cards. */
  compact?: boolean;
  testId?: string;
};

/** Shows context usage and compaction thresholds when token counts are known. */
export function ContextRing(props: ContextRingProps) {
  const frac = () => {
    const w = props.window ?? 0;
    const p = props.prompt ?? 0;
    if (w <= 0 || p <= 0) return undefined;
    return Math.min(1, p / w);
  };
  const tier = (): "ok" | "warn" | "critical" => {
    const f = frac() ?? 0;
    if (f >= 0.9) return "critical";
    const threshold = props.compactionThreshold ?? 0;
    if (threshold > 0 && (props.prompt ?? 0) >= threshold) return "warn";
    return "ok";
  };
  const pct = () => Math.round((frac() ?? 0) * 100);
  const title = () => {
    const p = props.prompt ?? 0;
    const w = props.window ?? 0;
    let s = `${p.toLocaleString()} / ${w.toLocaleString()} tokens (${pct()}% of context)`;
    const threshold = props.compactionThreshold ?? 0;
    if (threshold > 0) s += ` · compacts at ${threshold.toLocaleString()}`;
    return s;
  };

  return (
    <Show when={frac() != null}>
      <span
        class="context-ring"
        classList={{ "context-ring--compact": props.compact }}
        data-tier={tier()}
        data-testid={props.testId ?? "context-ring"}
        role="img"
        aria-label={title()}
        data-tip={title()}
      >
        <svg class="context-ring__svg" viewBox="0 0 18 18" aria-hidden="true">
          <circle class="context-ring__track" cx="9" cy="9" r="7" />
          <circle
            class="context-ring__fill"
            cx="9"
            cy="9"
            r="7"
            stroke-dasharray={`${(frac() ?? 0) * CONTEXT_RING_CIRCUMFERENCE} ${CONTEXT_RING_CIRCUMFERENCE}`}
          />
        </svg>
        <span class="context-ring__pct">{pct()}%</span>
      </span>
    </Show>
  );
}
