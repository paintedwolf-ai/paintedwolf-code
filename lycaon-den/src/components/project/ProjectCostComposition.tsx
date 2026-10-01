import { For, Show, type JSX } from "solid-js";
import type { CostSummary } from "../../api/types.ts";
import {
  formatTokenSplit,
  formatTokenSplitExact,
} from "../../cost/cost-format.ts";
import { formatNanoUsd } from "../../cost/nano-usd.ts";
import type { CostRole } from "../../cost/cost-roles.ts";
import {
  rolePercent,
  type CostCompositionBasis,
} from "./project-cost-model.ts";

/** 2π × r for the composition donut (r=42). */
const DONUT_CIRCUMFERENCE = 263.89;

function donutOffsetFor(
  roles: readonly CostRole[],
  summary: CostSummary,
  basis: CostCompositionBasis,
  index: number,
): number {
  const before = roles
    .slice(0, index)
    .reduce((total, role) => total + rolePercent(role, summary, basis), 0);
  return -(before / 100) * DONUT_CIRCUMFERENCE;
}

export function ProjectCostCompositionPanel(props: {
  title: string;
  basis: CostCompositionBasis;
  summary: CostSummary;
  roles: readonly CostRole[];
  empty?: string;
}): JSX.Element {
  const hasShares = () =>
    props.roles.some((role) => rolePercent(role, props.summary, props.basis) > 0);

  return (
    <section
      class="project-cost-composition__panel"
      data-testid={`project-cost-composition-${props.basis}`}
    >
      <h3>{props.title}</h3>
      <Show
        when={hasShares()}
        fallback={
          <p class="project-cost-composition__empty" role="status">
            {props.empty ?? "No share to chart yet."}
          </p>
        }
      >
        <div class="project-cost-composition__body">
          <svg
            class="project-cost-donut"
            viewBox="0 0 100 100"
            role="img"
            aria-label={props.roles
              .map(
                (role) =>
                  `${role.label} ${Math.round(rolePercent(role, props.summary, props.basis))}%`,
              )
              .join(", ")}
          >
            <circle cx="50" cy="50" r="42" class="project-cost-donut__track" />
            <For each={props.roles}>
              {(role, index) => (
                <circle
                  cx="50"
                  cy="50"
                  r="42"
                  class="project-cost-donut__segment"
                  data-role={role.id}
                  stroke-dasharray={`${(rolePercent(role, props.summary, props.basis) / 100) * DONUT_CIRCUMFERENCE} ${DONUT_CIRCUMFERENCE}`}
                  stroke-dashoffset={donutOffsetFor(
                    props.roles,
                    props.summary,
                    props.basis,
                    index(),
                  )}
                />
              )}
            </For>
          </svg>
          <div class="project-cost-legend">
            <For each={props.roles}>
              {(role) => (
                <div>
                  <i data-role={role.id} />
                  <span>{role.label}</span>
                  <strong>
                    {Math.round(rolePercent(role, props.summary, props.basis))}%
                  </strong>
                  <small data-tip={formatTokenSplitExact(role.totals)}>
                    {props.basis === "usd"
                      ? `${formatNanoUsd(role.nano_usd)} · ${formatTokenSplit(role.totals)}`
                      : formatTokenSplit(role.totals)}
                  </small>
                </div>
              )}
            </For>
          </div>
        </div>
      </Show>
    </section>
  );
}
