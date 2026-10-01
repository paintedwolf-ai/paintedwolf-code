import { For, Show } from "solid-js";
import type { FindingIgnoreListResponse, FindingIgnoreRule } from "../../api/types.ts";
import { ignoreEntrySummary } from "../../lib/ledger-display.ts";
import { relativizeScanUri } from "../../lib/scan-display.ts";

type Props = {
  catalog: FindingIgnoreListResponse | null;
  repoRoot?: string;
  pending: boolean;
  onWithdraw: (entryId: string) => void;
};

function coverageLine(rule: FindingIgnoreRule): string {
  if (rule.expired) return `Lapsed ${rule.expires_on} · matches nothing until renewed`;
  const count =
    rule.matches === 0
      ? "Matches no findings"
      : `${rule.matches} matching ${rule.matches === 1 ? "finding" : "findings"}`;
  return rule.expires_on ? `${count} · lapses ${rule.expires_on}` : count;
}

/** Every ignore entry with its match count, including lapsed and non-matching ones. */
export function IgnoreCatalogPanel(props: Props) {
  const rules = () => props.catalog?.rules ?? [];
  const projectRules = () => rules().filter((rule) => rule.source === "project");
  const bundledRules = () => rules().filter((rule) => rule.source === "bundled");
  const filePath = () => {
    const path = props.catalog?.path;
    return path ? relativizeScanUri(path, props.repoRoot) : "";
  };

  const row = (rule: FindingIgnoreRule) => (
    <li class="den-ignore-rules__row" data-testid="ignore-catalog-row">
      <span class="den-ignore-rules__row-text">
        <span class="den-ignore-rules__reason">{rule.reason}</span>
        <span class="den-ignore-rules__predicates">{ignoreEntrySummary(rule)}</span>
      </span>
      <span class="den-ignore-rules__coverage">{coverageLine(rule)}</span>
      <span class="den-ignore-rules__action">
        <Show
          when={rule.withdrawable}
          fallback={
            <Show when={rule.source === "project"}>
              <span data-testid="ignore-catalog-readonly">Edit in the file</span>
            </Show>
          }
        >
          <button
            type="button"
            class="den-ignore-rules__withdraw"
            data-testid="ignore-catalog-withdraw"
            disabled={props.pending}
            onClick={() => props.onWithdraw(rule.id)}
          >
            Withdraw
          </button>
        </Show>
      </span>
    </li>
  );

  return (
    <section class="den-ignore-rules" aria-label="Ignore rules" data-testid="ignore-catalog">
      <h2 class="den-ignore-rules__title">Ignore rules</h2>
      <p class="den-ignore-rules__body">
        A finding a rule matches is still scanned and recorded, but it leaves the Open list and
        the agent stops being told about it.
        <Show when={filePath()}>
          {" "}
          Rules are saved in <code class="den-ignore-rules__file">{filePath()}</code>, so they
          travel with the project.
        </Show>
      </p>

      <h3 class="den-ignore-rules__group">This project</h3>
      <Show
        when={projectRules().length > 0}
        fallback={
          <p class="den-ignore-rules__empty" data-testid="ignore-catalog-empty">
            No rules yet. To add one, select findings in the Open list and choose Ignore.
          </p>
        }
      >
        <ul class="den-ignore-rules__list">
          <For each={projectRules()}>{row}</For>
        </ul>
      </Show>

      <Show when={bundledRules().length > 0}>
        <h3 class="den-ignore-rules__group">Built in</h3>
        <p class="den-ignore-rules__note">
          These ship with the app, apply to every project, and can't be changed here.
        </p>
        <ul class="den-ignore-rules__list" data-testid="ignore-catalog-bundled">
          <For each={bundledRules()}>{row}</For>
        </ul>
      </Show>

    </section>
  );
}
