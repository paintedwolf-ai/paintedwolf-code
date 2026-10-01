import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { PreparedSurface } from "../../primitives/PreparedSurface.tsx";
import {
  createEffect,
  createMemo,
  createSignal,
  untrack,
  For,
  onCleanup,
  Show,
} from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  WebResearchProvidersResponse,
  WebResearchSettings,
} from "../../../api/types.ts";
import {
  WEB_RESEARCH_ACTIVITY_PAGE_SIZE,
  webResearchActivityToDenseItems,
} from "../../../settings/research/web-research-activity.ts";
import { resolveInitialEnabledProviderIds } from "../../../settings/research/web-research-providers-model.ts";
import { prefsSnapshot } from "../../../settings/research/web-research-enabled-prefs.ts";
import {
  WEB_RESEARCH_SETTINGS_COPY,
  type WebResearchSettingsTab,
} from "../../../settings/research/web-research-settings-copy.ts";
import { followSettingRevealTab } from "../../../settings/settings-reveal.ts";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";
import { clearWebIndexViaLocalData } from "../../../settings/storage/local-data-actions.ts";
import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenseReadOnlyList } from "../DenseReadOnlyList.tsx";
import {
  SettingsEditorTitle,
  WebResearchIcon,
} from "../SettingsEditorTitle.tsx";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import { WebResearchProvidersPanel } from "./WebResearchProvidersPanel.tsx";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";

type Props = {
  client: LycaonClient;
  initialTab?: WebResearchSettingsTab;
};

const TABS: { id: WebResearchSettingsTab; label: string }[] = [
  { id: "providers", label: WEB_RESEARCH_SETTINGS_COPY.tabProviders },
  { id: "index", label: WEB_RESEARCH_SETTINGS_COPY.tabIndex },
];

export function WebResearchSettingsPanel(props: Props) {
  const [tab, setTab] = createSignal<WebResearchSettingsTab>(
    props.initialTab ?? "providers",
  );
  followSettingRevealTab("web-research", setTab);
  const providers = createSurfaceQuery({
    name: "web-research-providers",
    source: () => ({ client: props.client, key: "device" }),
    load: ({ client }) => client.getWebResearchProviders(),
  });
  const settingsQuery = createSurfaceQuery({
    name: "web-research-settings",
    source: () => ({ client: props.client, key: "device" }),
    load: ({ client }) => client.getWebResearchSettings(),
  });
  const index = createSurfaceQuery({
    name: "web-research-index",
    source: () => ({ client: props.client, key: "device" }),
    load: ({ client }) => client.getWebResearchIndex(),
    required: false,
  });
  const status = providers.value;
  const settings = settingsQuery.value;
  const indexStatus = index.value;
  const loadError = () => providers.error() || settingsQuery.error();
  const [searchEnabled, setSearchEnabled] = createSignal(true);
  const [warmingOn, setWarmingOn] = createSignal(true);
  const [domainGuess, setDomainGuess] = createSignal(true);
  const [enabledIds, setEnabledIds] = createSignal<Set<string>>(new Set());
  const [prefsError, setPrefsError] = createSignal<string | undefined>();
  const [clearingIndex, setClearingIndex] = createSignal(false);
  const [clearIndexError, setClearIndexError] = createSignal<
    string | undefined
  >();

  let disposed = false;
  let prefsRevision = 0;
  let pendingPrefs = 0;
  let prefsQueue = Promise.resolve();

  onCleanup(() => {
    disposed = true;
  });

  const syncPrefs = (next: WebResearchSettings) => {
    setSearchEnabled(next.search_enabled ?? true);
    setWarmingOn(next.warming ?? true);
    setDomainGuess(next.guess_domains ?? true);
    setEnabledIds(resolveInitialEnabledProviderIds(next));
  };
  const applyStatus = (next: WebResearchProvidersResponse | undefined) => {
    if (!next) return;
    providers.publish(next);
  };
  createEffect(() => {
    const next = settings();
    if (next) untrack(() => {
      if (pendingPrefs === 0) syncPrefs(next);
    });
  });

  const queuePrefs = (snap: ReturnType<typeof prefsSnapshot>) => {
    const revision = ++prefsRevision;
    pendingPrefs++;
    const client = props.client;
    prefsQueue = prefsQueue.then(async () => {
      try {
        const next = await client.updateWebResearchSettings(snap);
        if (disposed || revision !== prefsRevision) return;
        setPrefsError(undefined);
        settingsQuery.publish(next);
        syncPrefs(next);
      } catch (err) {
        if (disposed || revision !== prefsRevision) return;
        setPrefsError(
          err instanceof Error
            ? err.message
            : WEB_RESEARCH_SETTINGS_COPY.prefsError,
        );
        const next = await settingsQuery.refresh();
        if (next && !disposed && revision === prefsRevision) syncPrefs(next);
      } finally {
        pendingPrefs--;
      }
    });
  };

  const setWarmingEnabled = (on: boolean) => {
    setWarmingOn(on);
    const guess = on ? domainGuess() : false;
    if (!on) setDomainGuess(guess);
    queuePrefs(prefsSnapshot(on, guess, searchEnabled(), enabledIds()));
  };

  const setDomainGuessEnabled = (on: boolean) => {
    setDomainGuess(on);
    const warming = on ? true : warmingOn();
    if (on) setWarmingOn(warming);
    queuePrefs(prefsSnapshot(warming, on, searchEnabled(), enabledIds()));
  };

  const setSearchEnabledPref = (on: boolean) => {
    setSearchEnabled(on);
    queuePrefs(prefsSnapshot(warmingOn(), domainGuess(), on, enabledIds()));
  };

  const addProvider = (id: string) => {
    const next = new Set(enabledIds());
    next.add(id);
    setEnabledIds(next);
    queuePrefs(
      prefsSnapshot(warmingOn(), domainGuess(), searchEnabled(), next),
    );
  };

  const removeProvider = (id: string) => {
    const next = new Set(enabledIds());
    next.delete(id);
    setEnabledIds(next);
    queuePrefs(
      prefsSnapshot(warmingOn(), domainGuess(), searchEnabled(), next),
    );
  };

  const availableIndexStatus = () => {
    const idx = indexStatus();
    return idx?.available ? idx : undefined;
  };

  const activityItems = createMemo(() => {
    const idx = availableIndexStatus();
    return idx ? webResearchActivityToDenseItems(idx.activity) : [];
  });

  const confirmClearIndex = (): Promise<boolean> =>
    confirmDestructive({
      message: WEB_RESEARCH_SETTINGS_COPY.clearIndexConfirm,
      title: WEB_RESEARCH_SETTINGS_COPY.clearIndex,
      okLabel: WEB_RESEARCH_SETTINGS_COPY.clearIndex,
    });

  const clearIndex = async () => {
    if (!(await confirmClearIndex())) return;
    const target = index.capture();
    setClearingIndex(true);
    setClearIndexError(undefined);
    try {
      target.publish(await clearWebIndexViaLocalData(props.client));
    } catch (err) {
      setClearIndexError(
        err instanceof Error
          ? err.message
          : WEB_RESEARCH_SETTINGS_COPY.clearIndexError,
      );
    } finally {
      setClearingIndex(false);
    }
  };

  const currentStatus = (): WebResearchProvidersResponse => {
    const current = status();
    if (!current) throw new Error("web research status unavailable");
    return current;
  };

  return (
    <div class="den-settings-editor" data-testid="web-research-settings-panel">
      <SettingsEditorTitle icon={<WebResearchIcon />}>
        {WEB_RESEARCH_SETTINGS_COPY.title}
      </SettingsEditorTitle>

      <Show when={loadError()}>
        <p class="den-settings-warn" data-testid="web-research-load-error" role="alert">
          {loadError()}
        </p>
      </Show>

      <Show when={providers.showLoading()}>
        <p class="den-settings-hint">Loading…</p>
      </Show>

      <Show when={status() !== undefined}>
        <>
            <UnderlineTabs
              aria-label={WEB_RESEARCH_SETTINGS_COPY.tabsAriaLabel}
            >
              <For each={TABS}>
                {(t) => (
                  <button
                    type="button"
                    role="tab"
                    id={`web-research-tab-${t.id}`}
                    aria-selected={tab() === t.id}
                    aria-controls={`web-research-panel-${t.id}`}
                    class="den-underline-tab"
                    data-active={tab() === t.id}
                    data-testid={`web-research-tab-${t.id}`}
                    onClick={() => setTab(t.id)}
                  >
                    {t.label}
                  </button>
                )}
              </For>
            </UnderlineTabs>

            <div
              id="web-research-panel-providers"
              role="tabpanel"
              aria-labelledby="web-research-tab-providers"
              hidden={tab() !== "providers"}
              data-testid="web-research-panel-providers"
              class="den-settings-web-research-tab"
            >
              <div
                class="den-settings-prefs-band"
                data-testid="web-research-enable"
              >
                <div class="den-settings-pref-row" {...settingAnchor("web-research")}>
                  <div class="den-settings-pref-copy">
                    <span class="den-settings-pref-label">
                      {settingLabel("web-research")}
                    </span>
                    <p class="den-settings-hint">
                      {WEB_RESEARCH_SETTINGS_COPY.intro}
                    </p>
                    <Show when={!searchEnabled()}>
                      <p
                        class="den-settings-hint"
                        data-testid="web-research-disabled-note"
                      >
                        {WEB_RESEARCH_SETTINGS_COPY.disabledNote}
                      </p>
                    </Show>
                    <Show when={prefsError()}>
                      <p class="den-settings-warn" role="alert">{prefsError()}</p>
                    </Show>
                  </div>
                  <DenCheckbox
                    checked={searchEnabled()}
                    data-testid="web-research-enabled-toggle"
                    onChange={(e) =>
                      setSearchEnabledPref(e.currentTarget.checked)}
                  >
                    <span class="sr-only">
                      {settingLabel("web-research")}
                    </span>
                  </DenCheckbox>
                </div>
              </div>

              <SettingsGovernedGroup
                label={WEB_RESEARCH_SETTINGS_COPY.tabProviders}
                active={searchEnabled()}
                inset={false}
                testId="web-research-details"
              >
                <WebResearchProvidersPanel
                  client={props.client}
                  status={currentStatus()}
                  enabledIds={enabledIds()}
                  domainGuess={domainGuess()}
                  onDomainGuessChange={setDomainGuessEnabled}
                  onStatus={applyStatus}
                  onAddProvider={addProvider}
                  onRemoveProvider={removeProvider}
                />
              </SettingsGovernedGroup>
            </div>

            <div
              id="web-research-panel-index"
              role="tabpanel"
              aria-labelledby="web-research-tab-index"
              hidden={tab() !== "index"}
              data-testid="web-research-panel-index"
              class="den-settings-web-research-index"
            >
              <PreparedSurface name="web-research-index" ready={index.ready} required={props.initialTab === "index"}>
              <Show when={index.error()}><p class="den-settings-warn" role="alert">{index.error()}</p></Show>
              <SettingsGovernedGroup
                label={WEB_RESEARCH_SETTINGS_COPY.tabIndex}
                active={searchEnabled()}
                inset={false}
              >
                <section
                  class="den-approvals-block"
                  data-testid="web-research-index-section"
                >
                  <h3 class="den-settings-overline">
                    {WEB_RESEARCH_SETTINGS_COPY.indexHeading}
                  </h3>
                  <p class="den-settings-hint">
                    {WEB_RESEARCH_SETTINGS_COPY.indexIntro}
                  </p>

                  <div class="den-settings-pref-group">
                    <div
                      class="den-settings-pref-row"
                      data-testid="web-research-warming-row"
                      {...settingAnchor("web-research-warming")}
                    >
                      <div class="den-settings-pref-copy">
                        <span class="den-settings-pref-label">
                          {settingLabel("web-research-warming")}
                        </span>
                        <p class="den-settings-hint">
                          {WEB_RESEARCH_SETTINGS_COPY.warmingHint}
                        </p>
                      </div>
                      <DenCheckbox
                        checked={warmingOn()}
                        disabled={!searchEnabled()}
                        data-testid="web-research-warming-toggle"
                        onChange={(e) =>
                          setWarmingEnabled(e.currentTarget.checked)}
                      >
                        <span class="sr-only">
                          {settingLabel("web-research-warming")}
                        </span>
                      </DenCheckbox>
                    </div>

                    <ShowLatest
                      when={availableIndexStatus()}
                      fallback={
                        <div
                          class="den-settings-pref-row"
                          {...settingAnchor("web-research-index-storage")}
                        >
                          <div class="den-settings-pref-copy">
                            <span class="den-settings-pref-label">
                              {settingLabel("web-research-index-storage")}
                            </span>
                            <p class="den-settings-hint">
                              {WEB_RESEARCH_SETTINGS_COPY.indexUnavailable}
                            </p>
                          </div>
                        </div>
                      }
                    >
                      {(idx) => (
                        <div
                          class="den-settings-pref-row"
                          data-testid="web-research-storage-row"
                          {...settingAnchor("web-research-index-storage")}
                        >
                          <div class="den-settings-pref-copy">
                            <span class="den-settings-pref-label">
                              {settingLabel("web-research-index-storage")}
                            </span>
                            <p
                              class="den-settings-hint"
                              data-testid="web-research-index-stats"
                            >
                              {idx().docs === 0 && idx().hosts === 0
                                ? WEB_RESEARCH_SETTINGS_COPY.indexStorageEmpty
                                : WEB_RESEARCH_SETTINGS_COPY.indexStatsSummary({
                                  pages: idx().docs,
                                  hosts: idx().hosts,
                                  megabytes: Math.round(idx().bytes / (1 << 20)),
                                })}
                            </p>
                            <Show when={idx().docs > 0 || idx().hosts > 0}>
                              <p class="den-settings-hint">
                                {WEB_RESEARCH_SETTINGS_COPY.indexStatsDetail({
                                  verified: idx().verified,
                                  warmed: idx().warmed,
                                  warmHits: idx().warm_hits,
                                })}
                              </p>
                            </Show>
                            <p
                              class="den-settings-hint"
                              data-testid="web-research-index-health"
                            >
                              {WEB_RESEARCH_SETTINGS_COPY.indexHealthLabel}
                              {": "}
                              {WEB_RESEARCH_SETTINGS_COPY.indexHealthSummary({
                                writesApplied: idx().health.writes_applied,
                                writesDropped: idx().health.writes_dropped,
                                lastSearchMs: idx().health.last_search_ms,
                                maxSearchMs: idx().health.max_search_ms,
                              })}
                            </p>
                            <Show when={clearIndexError()}>
                              <p
                                class="den-settings-warn"
                                data-testid="web-research-clear-index-error"
                                role="alert"
                              >
                                {clearIndexError()}
                              </p>
                            </Show>
                          </div>
                          <div class="den-settings-pref-control">
                            <DenButton
                              variant="secondary"
                              data-testid="web-research-clear-index"
                              disabled={clearingIndex() || idx().docs === 0}
                              onClick={() => void clearIndex()}
                            >
                              {clearingIndex()
                                ? WEB_RESEARCH_SETTINGS_COPY.clearingIndex
                                : WEB_RESEARCH_SETTINGS_COPY.clearIndex}
                            </DenButton>
                          </div>
                        </div>
                      )}
                    </ShowLatest>
                  </div>
                </section>

                <Show when={availableIndexStatus()}>
                  <section
                    class="den-approvals-block"
                    data-testid="web-research-activity"
                  >
                    <h3 class="den-settings-overline">
                      {WEB_RESEARCH_SETTINGS_COPY.indexActivityHeading}
                    </h3>
                    <DenseReadOnlyList
                      items={activityItems()}
                      pageSize={WEB_RESEARCH_ACTIVITY_PAGE_SIZE}
                      emptyLabel={WEB_RESEARCH_SETTINGS_COPY.indexActivityEmpty}
                      ariaLabel={WEB_RESEARCH_SETTINGS_COPY
                        .indexActivityAriaLabel}
                      testId="web-research-activity-list"
                    />
                  </section>
                </Show>
              </SettingsGovernedGroup>
              </PreparedSurface>
            </div>
        </>
      </Show>
    </div>
  );
}
