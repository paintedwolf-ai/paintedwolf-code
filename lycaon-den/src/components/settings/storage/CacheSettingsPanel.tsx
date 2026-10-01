import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { For, Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { LocalDataBucketStatus } from "../../../api/types.ts";
import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";
import {
  CACHE_SETTINGS_COPY,
  cacheBucketCopy,
  cacheClearAllConfirm,
  cacheClearBucketConfirm,
  cachePresenceLabel,
} from "../../../settings/storage/cache-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";

type Props = {
  client: LycaonClient | null | undefined;
};

/** Advanced → Cache: rebuildable bucket clear UI. */
export function CacheSettingsPanel(props: Props) {
  const query = createSurfaceQuery({
    name: "local-data",
    source: () => props.client ? { client: props.client, key: "device" } : null,
    load: ({ client }) => client.getLocalData(),
  });
  const status = query.value;
  const loadError = query.error;
  const [busyId, setBusyId] = createSignal<string | null>(null);
  const [statusMessage, setStatusMessage] = createSignal<string | undefined>();
  const [actionError, setActionError] = createSignal<string | undefined>();

  const reload = query.refresh;

  const confirmClear = (
    title: string,
    message: string,
    okLabel: string,
  ): Promise<boolean> =>
    confirmDestructive({
      message,
      title,
      okLabel,
      cancelLabel: CACHE_SETTINGS_COPY.cancel,
    });

  const clearTargets = async (
    bucketIds: LocalDataBucketStatus["id"][],
    workspaceCacheIds: string[],
    okLabel: string,
    confirmMsg: string,
  ) => {
    const c = props.client;
    const targetCount = bucketIds.length + workspaceCacheIds.length;
    if (!c || targetCount === 0) return;
    const title =
      targetCount > 1
        ? CACHE_SETTINGS_COPY.clearAllConfirmTitle
        : CACHE_SETTINGS_COPY.clearBucketTitle;
    if (!(await confirmClear(title, confirmMsg, okLabel))) return;

    const busyKey = targetCount > 1 ? "__all__" : (bucketIds[0] ?? "__all__");
    setBusyId(busyKey);
    setActionError(undefined);
    setStatusMessage(undefined);
    try {
      const res = await c.clearLocalData({
        ...(bucketIds.length > 0 ? { buckets: bucketIds } : {}),
        ...(workspaceCacheIds.length > 0
          ? { workspace_cache_ids: workspaceCacheIds }
          : {}),
      });
      const failed = [
        ...res.results.filter((result) => !result.ok),
        ...res.workspace_cache_results.filter((result) => !result.ok),
      ];
      if (failed.length > 0) {
        setActionError(
          failed.map((r) => r.message || r.code || r.id).join("; ") ||
            CACHE_SETTINGS_COPY.clearError,
        );
      } else {
        setStatusMessage(CACHE_SETTINGS_COPY.cleared);
      }
      await reload();
    } catch (err) {
      setActionError(
        err instanceof Error ? err.message : CACHE_SETTINGS_COPY.clearError,
      );
    } finally {
      setBusyId(null);
    }
  };

  const onClearBucket = (bucket: LocalDataBucketStatus) => {
    const copy = cacheBucketCopy(bucket.id);
    void clearTargets(
      [bucket.id],
      [],
      copy.clearLabel,
      cacheClearBucketConfirm(bucket.id),
    );
  };

  const onClearWorkspaceCache = async (id: string, sourceRoot: string) => {
    const c = props.client;
    if (!c) return;
    if (
      !(await confirmClear(
        "Clear workspace cache",
        `Clear the reusable workspace cache for “${sourceRoot}”? Active and completed worker branches are kept. The cache will be rebuilt if needed.`,
        "Clear cache",
      ))
    ) {
      return;
    }
    setBusyId(`workspace:${id}`);
    setActionError(undefined);
    setStatusMessage(undefined);
    try {
      const res = await c.clearLocalData({ workspace_cache_ids: [id] });
      const failed = res.workspace_cache_results.filter((result) => !result.ok);
      if (failed.length > 0) {
        setActionError(
          failed.map((result) => result.message || result.code || result.id).join("; "),
        );
      } else {
        setStatusMessage(CACHE_SETTINGS_COPY.cleared);
      }
      await reload();
    } catch (err) {
      setActionError(
        err instanceof Error ? err.message : CACHE_SETTINGS_COPY.clearError,
      );
    } finally {
      setBusyId(null);
    }
  };

  const onClearAll = () => {
    const buckets = status()?.buckets ?? [];
    const cacheIds = (status()?.workspace_caches ?? []).map((cache) => cache.id);
    const labels = buckets.map((b) => cacheBucketCopy(b.id).label);
    if (cacheIds.length > 0) labels.push("Reusable workspace caches");
    void clearTargets(
      buckets.map((bucket) => bucket.id),
      cacheIds,
      CACHE_SETTINGS_COPY.clearAll,
      cacheClearAllConfirm(labels),
    );
  };

  const anyPresent = () =>
    (status()?.buckets ?? []).some((bucket) => bucket.present) ||
    (status()?.workspace_caches.length ?? 0) > 0;

  return (
    <section class="den-settings-section" data-testid="cache-settings">
      <p class="den-settings-hint">{CACHE_SETTINGS_COPY.intro}</p>

      <Show when={!props.client}>
        <p class="den-settings-hint" data-testid="cache-connect-hint">
          {CACHE_SETTINGS_COPY.connectHint}
        </p>
      </Show>

      <Show when={props.client}>
        <Show when={loadError()}>
          <p class="den-settings-warn" data-testid="cache-load-error" role="alert">
            {loadError()}
          </p>
        </Show>
        <Show when={actionError()}>
          <p class="den-settings-warn" data-testid="cache-clear-error" role="alert">
            {actionError()}
          </p>
        </Show>
        <Show when={statusMessage()}>
          <p
            class="den-settings-hint"
            data-testid="cache-clear-status"
            role="status"
          >
            {statusMessage()}
          </p>
        </Show>
        <Show when={query.showLoading()}>
          <p class="den-settings-hint">Loading…</p>
        </Show>

        <Show when={(status()?.buckets.length ?? 0) > 0}>
          <div class="den-settings-pref-group">
            <ul class="den-settings-pref-list" data-testid="cache-bucket-list">
              <For each={status()?.buckets ?? []}>
                {(bucket) => {
                  const copy = () => cacheBucketCopy(bucket.id);
                  const busy = () =>
                    busyId() === bucket.id || busyId() === "__all__";
                  const presence = () =>
                    cachePresenceLabel(bucket.present, bucket.bytes);
                  return (
                    <li
                      class="den-settings-pref-row"
                      data-testid={`cache-bucket-${bucket.id}`}
                    >
                      <div class="den-settings-pref-copy">
                        <div class="den-settings-pref-label-row">
                          <span class="den-settings-pref-label">
                            {copy().label}
                          </span>
                          <span
                            class="den-settings-pref-badge"
                            data-testid={`cache-presence-${bucket.id}`}
                            data-present={bucket.present ? "true" : "false"}
                          >
                            {presence()}
                          </span>
                        </div>
                        <p class="den-settings-hint">{copy().hint}</p>
                      </div>
                      <div class="den-settings-pref-control">
                        <DenButton
                          type="button"
                          variant="ghost"
                          compact
                          disabled={busy() || !bucket.present}
                          data-testid={`cache-clear-${bucket.id}`}
                          aria-label={copy().clearLabel}
                          onClick={() => onClearBucket(bucket)}
                        >
                          {busyId() === bucket.id
                            ? CACHE_SETTINGS_COPY.clearing
                            : CACHE_SETTINGS_COPY.clearAction}
                        </DenButton>
                      </div>
                    </li>
                  );
                }}
              </For>
            </ul>

            <div
              class="den-settings-pref-row"
              data-testid="cache-clear-all-row"
              {...settingAnchor("clear-caches")}
            >
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">
                  {settingLabel("clear-caches")}
                </span>
                <p class="den-settings-hint">
                  {CACHE_SETTINGS_COPY.clearAllHint}{" "}
                  {CACHE_SETTINGS_COPY.durableKeepNote}
                </p>
              </div>
              <div class="den-settings-pref-control">
                <DenButton
                  type="button"
                  variant="danger"
                  compact
                  disabled={busyId() !== null || !anyPresent()}
                  data-testid="cache-clear-all"
                  aria-label={settingLabel("clear-caches")}
                  onClick={() => onClearAll()}
                >
                  {busyId() === "__all__"
                    ? CACHE_SETTINGS_COPY.clearing
                    : CACHE_SETTINGS_COPY.clearAll}
                </DenButton>
              </div>
            </div>
          </div>
        </Show>

        <Show when={(status()?.workspace_caches.length ?? 0) > 0}>
          <div class="den-settings-subsection">
            <h3 class="den-settings-subhead">Reusable workspace caches</h3>
            <p class="den-settings-hint">
              Retained only when this device can clone cached files into worker
              workspaces without copying their data again. Unused entries expire
              automatically after 30 days.
            </p>
            <div class="den-settings-pref-group">
              <ul class="den-settings-pref-list" data-testid="workspace-cache-list">
                <For each={status()?.workspace_caches ?? []}>
                  {(cache) => {
                    const busy = () => busyId() === `workspace:${cache.id}`;
                    const logical = () =>
                      cachePresenceLabel(true, cache.logical_bytes);
                    const allocated = () =>
                      cachePresenceLabel(true, cache.allocated_bytes);
                    return (
                      <li
                        class="den-settings-pref-row"
                        data-testid={`workspace-cache-${cache.id}`}
                      >
                        <div class="den-settings-pref-copy">
                          <span class="den-settings-pref-label">
                            {cache.source_root}
                          </span>
                          <p class="den-settings-hint">
                            {logical()} logical · {allocated()} retained · last
                            used{" "}
                            {new Date(cache.last_used_at).toLocaleDateString()}
                          </p>
                        </div>
                        <div class="den-settings-pref-control">
                          <DenButton
                            type="button"
                            variant="ghost"
                            compact
                            disabled={busy()}
                            data-testid={`workspace-cache-clear-${cache.id}`}
                            onClick={() =>
                              void onClearWorkspaceCache(
                                cache.id,
                                cache.source_root,
                              )
                            }
                          >
                            {busy()
                              ? CACHE_SETTINGS_COPY.clearing
                              : CACHE_SETTINGS_COPY.clearAction}
                          </DenButton>
                        </div>
                      </li>
                    );
                  }}
                </For>
              </ul>
            </div>
          </div>
        </Show>
      </Show>
    </section>
  );
}
