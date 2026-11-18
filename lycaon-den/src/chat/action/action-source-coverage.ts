import { createEffect, createMemo, createSignal, onCleanup, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { ActionFact } from "../../api/types.ts";
import { subscribeSourceInvalidation } from "../../files/source/source-invalidation.ts";
import { BOUNDARY_REASONS, GAP_REASONS } from "../../files/source/source-command-window.ts";
import { withSourceObservation, type CardAction } from "./action-merge.ts";

type FolderCoverage = NonNullable<ActionFact["source_coverage"]>[number];

export function useActionCoverage(
  action: Accessor<CardAction>,
  scope: { client?: LycaonClient | null; projectId?: string; sessionId?: string },
): Accessor<CardAction> {
  const observation = () => action().facts.find((fact) => fact.kind === "source_observation");
  const windows = createMemo(() => observation()?.source_windows?.join(",") ?? "");
  const address = createMemo(() => {
    const { client, projectId, sessionId } = scope;
    const ids = windows();
    return client && projectId && sessionId && ids ? { client, projectId, sessionId, ids } : null;
  });
  const [loaded, setLoaded] = createSignal<{ address: ReturnType<typeof address>; coverage: FolderCoverage[] }>();
  createEffect(() => {
    const requested = address();
    if (!requested) return;
    const { client, projectId, sessionId, ids } = requested;
    const controller = new AbortController();
    const pending = new Set(ids.split(","));
    const coverage = new Map<string, FolderCoverage[]>();
    let refreshing = false;
    let again = false;
    const refresh = async () => {
      if (refreshing) {
        again = true;
        return;
      }
      refreshing = true;
      do {
        again = false;
        await Promise.all([...pending].map(async (id) => {
          try {
            const state = await client.getProjectSourceCommandObservation(projectId, sessionId, id, controller.signal);
            coverage.set(id, state.coverage);
            if (!state.settling) pending.delete(id);
          } catch (error) {
            // Settled windows with no history or gaps are pruned.
            if (error instanceof LycaonApiError && error.status === 404) pending.delete(id);
          }
        }));
        if (controller.signal.aborted) return;
        setLoaded({ address: requested, coverage: [...coverage.values()].flat() });
      } while (again && pending.size);
      refreshing = false;
    };
    const unsubscribe = subscribeSourceInvalidation((event) => {
      if (event.projectId === projectId && pending.size) void refresh();
    });
    onCleanup(() => {
      unsubscribe();
      controller.abort();
    });
    void refresh();
  });
  return createMemo(() => {
    const current = action();
    const fact = observation();
    if (!fact?.source_windows?.length) return current;
    const resolved = loaded();
    const coverage = [...(fact.source_coverage ?? []), ...(resolved && resolved.address === address() ? resolved.coverage : [])];
    return withSourceObservation(current, coverage.length ? { ...fact, source_coverage: coverage } : undefined);
  });
}

export function activityCoverageNotices(coverage: readonly FolderCoverage[] = []): string[] {
  const notices = new Set<string>();
  for (const entry of coverage) {
    const folder = entry.folder === "/" && entry.reason === "unconfined" ? "Everything outside the project" : entry.folder;
    if (entry.coverage === "not_observed") {
      const reason = entry.reason ? GAP_REASONS[entry.reason] : "no reason was recorded";
      notices.add(`${folder} was not observed: ${reason}.`);
    } else if (entry.coverage === "partial") {
      const skipped = (entry.boundaries ?? []).map(({ path, reason }) =>
        `${path === "." ? folder : path} (${BOUNDARY_REASONS[reason]})`);
      if (entry.omitted_boundaries) skipped.push(`${entry.omitted_boundaries.toLocaleString()} more`);
      notices.add(skipped.length
        ? `Not every folder in ${folder} was compared. Skipped ${skipped.join(", ")}.`
        : `Not every folder in ${folder} was compared.`);
    }
  }
  return [...notices];
}
