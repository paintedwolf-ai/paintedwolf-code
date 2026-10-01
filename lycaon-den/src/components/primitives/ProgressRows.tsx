import { Index, Show } from "solid-js";

export type ProgressRow = {
  key: string;
  label: string;
  status: string;
  /** Fraction done in [0, 1]. */
  ratio: number;
  /** Whether the ratio is known, so the bar may state a value. */
  measured: boolean;
  readout?: string;
  /** The unit's machine state, exposed as `data-state`. */
  state?: string;
};

type Props = {
  rows: readonly ProgressRow[];
  testId?: string;
  statusTestId?: string;
  readoutTestId?: string;
};

/** One row per unit of host work: its label, status, bar, and a readout of how far it has come. */
export function ProgressRows(props: Props) {
  return (
    <Show when={props.rows.length > 0}>
      <ul class="den-progress-rows" data-testid={props.testId}>
        <Index each={props.rows}>
          {(row) => (
            <li data-state={row().state} data-row-key={row().key}>
              <p class="den-progress-rows__head">
                <span class="den-progress-rows__label">{row().label}</span>
                <span class="den-progress-rows__status" data-testid={props.statusTestId}>
                  {row().status}
                </span>
              </p>
              <div
                class="den-progress-rows__bar"
                role="progressbar"
                aria-label={`${row().label} progress`}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={row().measured ? Math.round(row().ratio * 100) : undefined}
              >
                <span class="den-progress-rows__fill" style={{ width: `${row().ratio * 100}%` }} />
              </div>
              <Show when={row().readout}>
                {(readout) => (
                  <p class="den-progress-rows__readout" data-testid={props.readoutTestId}>
                    {readout()}
                  </p>
                )}
              </Show>
            </li>
          )}
        </Index>
      </ul>
    </Show>
  );
}
