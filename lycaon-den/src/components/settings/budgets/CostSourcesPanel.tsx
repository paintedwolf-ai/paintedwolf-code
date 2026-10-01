import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";
import { For, Show, createSignal } from "solid-js";
import { unwrap } from "solid-js/store";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  PricingSourceMeta,
  SettingsPricing,
  SettingsPricingResponse,
  SettingsPricingSource,
} from "../../../api/types.ts";
import { COST_SETTINGS_COPY as C } from "../../../settings/budgets/cost-settings-copy.ts";
import {
  freshnessLabel,
  hasSelectedSource,
  selectPricingSource,
  statusBadge,
  withPendingPricing,
} from "../../../settings/budgets/cost-sources-model.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenRadio } from "../../primitives/DenRadio.tsx";
import { CostIcon, SettingsEditorTitle } from "../SettingsEditorTitle.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";

type Props = {
  client: LycaonClient;
  settingsStore: SettingsStore;
};

function metaFor(
  pricing: SettingsPricingResponse,
  src: SettingsPricingSource,
): PricingSourceMeta {
  return (
    pricing.available_sources.find((s) => s.id === src.id) ?? {
      id: src.id,
      label: src.id,
      kind: src.id,
      enabled: src.enabled,
      status: "offline",
    }
  );
}

/**
 * Settings → Cost. Main toggle plus one selected pricing feed; estimates only.
 * Choices apply immediately and save in order; the host fetches feeds in the
 * background and reports progress through `refreshing`.
 */
export function CostSourcesPanel(props: Props) {
  const [refreshingId, setRefreshingId] = createSignal<string | undefined>();
  const [needsSource, setNeedsSource] = createSignal(false);

  const pricing = () => props.settingsStore.state.pricing;

  let saves: Promise<void> = Promise.resolve();
  let latestSave = 0;
  let pendingSaves = 0;
  let confirmed: SettingsPricingResponse | undefined;

  const save = (next: SettingsPricing) => {
    const cur = pricing();
    if (!cur) return;
    // The store reconciles in place, so a snapshot must not alias its objects.
    if (pendingSaves === 0) confirmed = structuredClone(unwrap(cur));
    pendingSaves++;
    const mine = ++latestSave;
    setNeedsSource(false);
    props.settingsStore.actions.setError(undefined);
    props.settingsStore.actions.setPricing(withPendingPricing(cur, next));
    saves = saves.then(async () => {
      try {
        const updated = await props.client.updatePricingSettings(next);
        confirmed = structuredClone(updated);
        if (mine === latestSave) props.settingsStore.actions.setPricing(updated);
      } catch (err) {
        if (mine === latestSave && confirmed) {
          props.settingsStore.actions.setPricing(confirmed);
        }
        props.settingsStore.actions.setError(
          err instanceof Error ? err.message : C.saveError,
        );
      } finally {
        pendingSaves--;
      }
    });
  };

  const onMainChange = (target: HTMLInputElement) => {
    const cur = pricing();
    if (!cur) return;
    const enabled = target.checked;
    if (enabled && !hasSelectedSource(cur.sources)) {
      setNeedsSource(true);
      target.checked = false;
      return;
    }
    save({ cost_tracking_enabled: enabled, sources: cur.sources });
  };

  const onSelect = (id: string) => {
    const cur = pricing();
    if (!cur) return;
    save({
      cost_tracking_enabled: cur.cost_tracking_enabled,
      sources: selectPricingSource(cur.sources, id),
    });
  };

  const onRefresh = async (id: string) => {
    setRefreshingId(id);
    props.settingsStore.actions.setError(undefined);
    try {
      const meta = await props.client.refreshPricingSource(id);
      const cur = pricing();
      if (!cur) return;
      props.settingsStore.actions.setPricing({
        ...cur,
        available_sources: cur.available_sources.map((s) =>
          s.id === id ? meta : s,
        ),
      });
    } catch (err) {
      props.settingsStore.actions.setError(
        err instanceof Error ? err.message : C.saveError,
      );
    } finally {
      setRefreshingId(undefined);
    }
  };

  return (
    <div class="den-settings-editor" data-testid="cost-sources">
      <SettingsEditorTitle icon={<CostIcon />}>{C.title}</SettingsEditorTitle>

      <p class="den-settings-hint" data-testid="cost-sources-intro">
        {C.intro}
      </p>

      <Show when={pricing()}>
        {(cfg) => {
          const trackingOn = () => cfg().cost_tracking_enabled;
          return (
            <>
              <div class="den-settings-prefs-band">
                <div
                  class="den-settings-pref-row"
                  data-testid="cost-tracking-main-row"
                  {...settingAnchor("cost-tracking")}
                >
                  <div class="den-settings-pref-copy">
                    <span class="den-settings-pref-label">{settingLabel("cost-tracking")}</span>
                    <p class="den-settings-hint">{C.mainHint}</p>
                  </div>
                  <DenCheckbox
                    checked={trackingOn()}
                    data-testid="cost-tracking-toggle"
                    onChange={(e) => onMainChange(e.currentTarget)}
                  >
                    <span class="sr-only">{settingLabel("cost-tracking")}</span>
                  </DenCheckbox>
                </div>

                <Show when={needsSource()}>
                  <p
                    class="den-settings-warn"
                    data-testid="cost-tracking-needs-source"
                    role="status"
                  >
                    {C.needsSource}
                  </p>
                </Show>

                <Show when={!trackingOn()}>
                  <p
                    class="den-settings-hint"
                    data-testid="cost-tracking-off-hint"
                  >
                    {C.mainOffSourcesHint}
                  </p>
                </Show>
              </div>

              <h3
                id="cost-sources-heading"
                class="den-settings-pref-group-title"
                {...chromeProps()}
              >
                {C.sourcesHeading}
              </h3>
              <p class="den-settings-hint">{C.sourcesHint}</p>

              <div
                class="den-settings-pref-list"
                classList={{ "den-cost-sources--idle": !trackingOn() }}
                role="radiogroup"
                aria-labelledby="cost-sources-heading"
                data-testid="cost-sources-list"
              >
                {/* Rows key on id: saves replace source objects, and a rebuilt radio loses focus. */}
                <For each={cfg().sources.map((s) => s.id)}>
                  {(id) => {
                    const src = () =>
                      cfg().sources.find((s) => s.id === id) ?? { id, enabled: false };
                    const meta = () => metaFor(cfg(), src());
                    const badge = () => statusBadge(meta());
                    const fetching = () =>
                      meta().refreshing === true || refreshingId() === id;
                    const hint = () =>
                      fetching() && meta().status === "offline"
                        ? C.fetching
                        : (C.statusHint[badge().tone] ?? C.statusHint.offline);
                    return (
                      <div
                        class="den-settings-pref-row den-cost-source-card"
                        classList={{
                          "den-cost-source-card--idle": !trackingOn(),
                        }}
                        data-testid={`cost-source-card-${id}`}
                      >
                        <div class="den-settings-pref-copy">
                          <div class="den-settings-pref-label-row">
                            <DenRadio
                              name="cost-pricing-source"
                              value={id}
                              checked={src().enabled}
                              data-testid={`cost-source-select-${id}`}
                              onChange={() => onSelect(id)}
                            >
                              <span class="den-settings-pref-label">
                                {meta().label || id}
                              </span>
                            </DenRadio>
                            <span
                              class="den-settings-pref-badge"
                              data-tone={badge().tone}
                              data-testid={`cost-source-status-${id}`}
                            >
                              {badge().label}
                            </span>
                          </div>
                          <p
                            class="den-settings-hint"
                            data-testid={`cost-source-hint-${id}`}
                          >
                            {hint()} {C.freshnessPrefix}:{" "}
                            {freshnessLabel(meta())}
                          </p>
                        </div>
                        <Show when={src().enabled}>
                          <div class="den-settings-pref-control">
                            <div class="den-cost-source-actions">
                              <DenButton
                                variant="secondary"
                                compact
                                disabled={!trackingOn() || fetching()}
                                data-testid={`cost-source-refresh-${id}`}
                                onClick={() => void onRefresh(id)}
                              >
                                {fetching() ? C.refreshing : C.refresh}
                              </DenButton>
                            </div>
                          </div>
                        </Show>
                      </div>
                    );
                  }}
                </For>
              </div>
            </>
          );
        }}
      </Show>
    </div>
  );
}
