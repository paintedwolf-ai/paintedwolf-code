import { For, Show, createResource, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ApprovalRecentAskRow } from "../../../api/types.ts";
import { gateLabel } from "../../../chat/checkpoint/gate-copy.ts";
import { refreshApprovalGrantsCache } from "../../../settings/security/approval-grants-cache.ts";
import { SAVED_APPROVALS_COPY } from "../../../settings/security/saved-approvals-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";

type Props = {
  client: LycaonClient;
  days?: number;
};

// Recent approval counts come from the local authorization ledger.
// A grant is offered only when every subject names the same site.
export function RecentAsksStrip(props: Props) {
  const [rows, { refetch }] = createResource(
    () => props.days ?? 7,
    async (days) => {
      const res = await props.client.listApprovalAsks(days);
      return res.asks ?? (res as any).rows ?? [];
    },
  );
  const [busy, setBusy] = createSignal<string | undefined>();
  const [done, setDone] = createSignal<Record<string, string>>({});
  const [error, setError] = createSignal<string | undefined>();

  const widen = async (row: ApprovalRecentAskRow) => {
    const pattern = row.host_pattern?.trim();
    if (!pattern) return;
    setBusy(row.gate);
    setError(undefined);
    try {
      await props.client.createApprovalGrant({
        category: "host",
        scope: "device",
        host_pattern: pattern,
      });
      setDone((prev) => ({ ...prev, [row.gate]: pattern }));
      refreshApprovalGrantsCache();
      void refetch();
    } catch {
      setError(SAVED_APPROVALS_COPY.recentWidenError);
    } finally {
      setBusy(undefined);
    }
  };

  return (
    <section class="den-saved-approvals-recent" data-testid="saved-approvals-recent">
      <div class="den-saved-approvals-band-head">
        <h3>{SAVED_APPROVALS_COPY.recentTitle}</h3>
      </div>
      <p class="den-settings-hint">{SAVED_APPROVALS_COPY.recentExplain}</p>
      <Show when={error()} keyed>
        {(text) => (
          <p class="den-settings-hint" role="alert" data-testid="saved-approvals-recent-error">
            {text}
          </p>
        )}
      </Show>
      <Show
        when={!rows.loading}
        fallback={<p class="den-settings-hint">{SAVED_APPROVALS_COPY.recentLoading}</p>}
      >
        <Show when={!rows.error} fallback={<p class="den-settings-hint">{SAVED_APPROVALS_COPY.recentLoadError}</p>}>
          <Show
            when={(rows() ?? []).length > 0}
            fallback={
              <p class="den-settings-hint" data-testid="saved-approvals-recent-empty">
                {SAVED_APPROVALS_COPY.recentEmpty}
              </p>
            }
          >
            <ul class="den-saved-approvals-list">
              <For each={rows() ?? []}>
                {(row) => (
                  <li
                    class="den-saved-approvals-row"
                    data-testid="saved-approvals-recent-row"
                    data-gate={row.gate}
                  >
                    <div class="den-saved-approvals-row-main">
                      <span class="den-saved-approvals-title">{gateLabel(row.gate)}</span>
                      <p class="den-settings-hint">
                        {SAVED_APPROVALS_COPY.recentCounts(row.asks, row.allowed)}
                        <Show when={row.asks > 1 && row.allowed === row.asks}>
                          {" · "}
                          <span data-testid="saved-approvals-recent-all-allowed">
                            {SAVED_APPROVALS_COPY.recentAllAllowed}
                          </span>
                        </Show>
                      </p>
                      <Show when={(row.subjects ?? []).length > 0}>
                        <p class="den-settings-hint" data-testid="saved-approvals-recent-subjects">
                          {SAVED_APPROVALS_COPY.recentSubjects(row.subjects ?? [])}
                        </p>
                      </Show>
                    </div>
                    <Show when={row.host_pattern?.trim()} keyed>
                      {(pattern) => (
                        <Show
                          when={!done()[row.gate]}
                          fallback={
                            <span class="den-settings-hint" data-testid="saved-approvals-recent-widened">
                              {SAVED_APPROVALS_COPY.recentWidenDone(pattern)}
                            </span>
                          }
                        >
                          <DenButton
                            variant="secondary"
                            data-testid="saved-approvals-recent-widen"
                            disabled={busy() === row.gate}
                            onClick={() => void widen(row)}
                          >
                            {SAVED_APPROVALS_COPY.recentWiden(pattern)}
                          </DenButton>
                        </Show>
                      )}
                    </Show>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </Show>
      </Show>
    </section>
  );
}
