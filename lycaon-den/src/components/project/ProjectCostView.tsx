import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { CacheSavingsNote } from "../../cost/CacheSavingsNote.tsx";
import {
  For,
  batch,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  CostSummary,
  ProjectCostReport,
  SettingsLimitsResponse,
  SettingsPricingResponse,
} from "../../api/types.ts";
import {
  callCount,
  estimateView,
  formatTokenSplit,
  formatTokenSplitExact,
  formatTokens,
  formatTokensCompact,
  formatUSD,
  priced,
  sourceAsOfLabel,
  unreportedFreeCalls,
} from "../../cost/cost-format.ts";
import { CostAmount } from "../../cost/CostAmount.tsx";
import { costExportFilename } from "../../cost/cost-export.ts";
import { costRoles } from "../../cost/cost-roles.ts";
import { getCostStore, getLycaonClient } from "../../platform/connection/app-connection.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { changedLimitsFields } from "../../settings/budgets/limits-model.ts";
import { SPEND_CEILING_WARN_RATIO } from "../../settings/budgets/spend-ceiling-readout.ts";
import { NANO_PER_USD } from "../../cost/nano-usd.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { createThrottledAsyncScheduler } from "../../store/coalesced-async.ts";
import type { CostStore } from "../../store/cost-store.ts";
import { TablePager } from "../list/TablePager.tsx";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenField } from "../primitives/DenField.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { DenNumberInput } from "../primitives/DenNumberInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { ProjectCostCompositionPanel } from "./ProjectCostComposition.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { SettingsGovernedGroup } from "../settings/SettingsGovernedGroup.tsx";
import {
  buildProjectCostCSV,
  costUsesUsdBasis,
  hasCostUsage,
  totalTokens,
  type ProjectCostSort,
} from "./project-cost-model.ts";

type Props = {
  projectId: string;
  projectName: string;
  appStore: AppStore;
  client?: LycaonClient | null;
  costStore?: CostStore | null;
  back?: StageBack | null;
  onOpenSession: (sessionId: string) => void;
  onOpenCostSettings?: () => void;
};

const COST_PAGE_SIZE = 40;
const PROJECT_COST_REFRESH_MS = 2000;
/** Starting limit offered when the project has never set one. */
const DEFAULT_SPEND_LIMIT_USD = 5;
/** Warning share the wire accepts, as percent. */
const WARN_PERCENT_MIN = 10;
const WARN_PERCENT_MAX = 95;

type ProjectCostData = {
  report: ProjectCostReport;
  limits: SettingsLimitsResponse;
  globalLimits: SettingsLimitsResponse;
  pricing: SettingsPricingResponse;
};

export function ProjectCostView(props: Props) {
  const client = () => props.client ?? getLycaonClient() ?? null;
  let disposed = false;
  const [query, setQuery] = createSignal("");
  const [search, setSearch] = createSignal("");
  const [sort, setSort] = createSignal<ProjectCostSort>("cost");
  const [page, setPage] = createSignal(0);
  const [cursors, setCursors] = createSignal([""]);
  const [exporting, setExporting] = createSignal(false);
  const [exportError, setExportError] = createSignal<string>();
  const resetPage = () => { setPage(0); setCursors([""]); };
  createEffect(() => { props.projectId; resetPage(); });
  createEffect(() => {
    const value = query().trim();
    if (value === untrack(search)) return;
    const timer = setTimeout(() => batch(() => { setSearch(value); resetPage(); }), 250);
    onCleanup(() => clearTimeout(timer));
  });
  const reportOptions = () => ({ limit: COST_PAGE_SIZE, search: search(), sort: sort(), cursor: cursors()[page()] ?? "" });
  const reportKey = () => JSON.stringify([props.projectId, reportOptions()]);
  const [enabled, setEnabled] = createSignal(false);
  const [ceiling, setCeiling] = createSignal(String(DEFAULT_SPEND_LIMIT_USD));
  const [warningPercent, setWarningPercent] = createSignal(
    String(Math.round(SPEND_CEILING_WARN_RATIO * 100)),
  );
  const [softStop, setSoftStop] = createSignal(true);
  const [saveState, setSaveState] = createSignal<"idle" | "saving" | "saved">("idle");
  const [saveError, setSaveError] = createSignal<string>();

  const settingsQuery = createSurfaceQuery({
    name: "project-cost-settings",
    source: () => { const c = client(); return c ? { client: c, key: props.projectId } : null; },
    load: async ({ client: c, key: projectId }) => {
      const [limits, globalLimits, pricing] = await Promise.all([
        c.getLimitsSettings(projectId), c.getLimitsSettings(), c.getPricingSettings(),
      ]);
      return { limits, globalLimits, pricing };
    },
    required: false,
  });
  const costQuery = createSurfaceQuery({
    name: "project-cost",
    source: () => { const c = client(); return c ? { client: c, key: reportKey(), projectId: props.projectId, options: reportOptions() } : null; },
    scope: (source) => source.projectId,
    load: ({ client: c, projectId, options }) => c.getProjectCostReport(projectId, options),
    required: false,
  });
  const data = createMemo<ProjectCostData | undefined>(() => {
    const settings = settingsQuery.value(); const report = costQuery.value();
    return settings && report ? { ...settings, report } : undefined;
  });
  const refetch = () => Promise.all([costQuery.refresh(), settingsQuery.refresh()]);
  const showPage = (next: number) => {
    if (costQuery.loading() || next < 0) return;
    if (next > page()) {
      const cursor = costQuery.value()?.next_cursor;
      if (!cursor) return;
      setCursors((held) => [...held.slice(0, next), cursor]);
    }
    setPage(next);
  };

  // Refresh usage without resetting limit edits.
  const liveReport = createThrottledAsyncScheduler(async () => {
    const c = client();
    const projectId = props.projectId.trim();
    if (!c || !projectId) return;
    const key = reportKey();
    const report = await c
      .getProjectCostReport(projectId, reportOptions())
      .catch(() => undefined);
    if (
      !report ||
      disposed ||
      props.projectId.trim() !== projectId ||
      client() !== c || reportKey() !== key
    ) return;
    costQuery.publish(report);
  }, PROJECT_COST_REFRESH_MS);

  const costStore = props.costStore ?? getCostStore();
  if (costStore) {
    let activated = false;
    createResidentActivity(() => {
      if (activated) liveReport.schedule();
      activated = true;
      const releaseLiveRefresh = costStore.holdLiveRefresh();
      const stopInvalidated = costStore.onInvalidated(() => liveReport.schedule());
      return () => {
        stopInvalidated();
        releaseLiveRefresh();
      };
    });
  }
  onCleanup(() => {
    disposed = true;
    liveReport.cancel();
  });

  // Sync the form only from full loads.
  const loadedLimits = createMemo(() => data()?.limits);

  createEffect(() => {
    const limits = loadedLimits();
    if (!limits) return;
    setEnabled(limits.spend_ceiling_enabled === true);
    const ceilingUsd = limits.session_spend_ceiling_nano_usd != null
      ? limits.session_spend_ceiling_nano_usd / NANO_PER_USD
      : DEFAULT_SPEND_LIMIT_USD;
    setCeiling(String(ceilingUsd));
    setWarningPercent(
      String(Math.round((limits.spend_warning_ratio ?? SPEND_CEILING_WARN_RATIO) * 100)),
    );
    setSoftStop(limits.spend_soft_stop);
    setSaveState("idle");
    setSaveError(undefined);
  });

  const report = () => data()?.report;
  const summary = () => report()?.summary;
  const projectHasUsage = createMemo(() => {
    const current = report();
    return current ? hasCostUsage(current.summary) : false;
  });
  const projectUtilities = () => report()?.project_utilities;
  const projectUtilityUsage = createMemo(() => {
    const utility = projectUtilities();
    return utility && hasCostUsage(utility) ? utility : undefined;
  });
  const retiredSessions = () => report()?.retired_sessions;
  const retiredSessionUsage = createMemo(() => {
    const retired = retiredSessions();
    return retired && hasCostUsage(retired) ? retired : undefined;
  });
  const rows = () => report()?.sessions ?? [];
  const roles = createMemo(() => {
    const current = summary();
    return current ? costRoles(current) : [];
  });
  const workerTasks = () => report()?.worker_task_count ?? 0;
  const archivedSessions = () => report()?.archived_session_count ?? 0;

  const maxBarValue = createMemo(() => {
    const current = report();
    if (!current) return 0;
    if (costUsesUsdBasis(current.summary)) {
      return Math.max(
        current.max_session_nano_usd,
        current.project_utilities.estimated_nano_usd,
        current.retired_sessions.estimated_nano_usd,
      );
    }
    return Math.max(
      totalTokens(current.project_utilities),
      totalTokens(current.retired_sessions),
      current.max_session_tokens,
    );
  });

  const usageBarPercent = (cost: CostSummary) => {
    const max = maxBarValue();
    if (max <= 0) return 0;
    const currentSummary = summary();
    const value = currentSummary && costUsesUsdBasis(currentSummary)
      ? cost.estimated_nano_usd
      : totalTokens(cost);
    return Math.max(1.5, (value / max) * 100);
  };

  const barPercent = (row: ProjectCostReport["sessions"][number]) =>
    usageBarPercent(row.cost);

  const ceilingNumber = () => Number(ceiling());
  const warningNumber = () => Number(warningPercent());
  const deviceStop = () => {
    const limits = data()?.globalLimits;
    const amount = (limits?.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
    return limits?.spend_ceiling_enabled === true && amount > 0
      ? amount
      : undefined;
  };
  const deviceAllowsSoftStop = () => data()?.globalLimits.spend_soft_stop ?? true;
  const allowsLanding = () => softStop() && deviceAllowsSoftStop();
  const warnPercentValid = () =>
    Number.isFinite(warningNumber()) &&
    warningNumber() >= WARN_PERCENT_MIN &&
    warningNumber() <= WARN_PERCENT_MAX;
  const guardrailError = createMemo(() => {
    if (!enabled()) return undefined;
    if (!Number.isFinite(ceilingNumber()) || ceilingNumber() <= 0) {
      return "Enter a spend limit greater than $0.";
    }
    if (!warnPercentValid()) {
      return `Warning must be between ${WARN_PERCENT_MIN}% and ${WARN_PERCENT_MAX}%.`;
    }
    const requiredStop = deviceStop();
    if (requiredStop != null && ceilingNumber() > requiredStop) {
      return `A project spend limit cannot exceed the device limit of ${formatUSD(requiredStop)}.`;
    }
    return undefined;
  });

  const guardrailDirty = createMemo(() => {
    const limits = data()?.limits;
    if (!limits) return false;
    const currentCeilingUsd = limits.session_spend_ceiling_nano_usd != null
      ? limits.session_spend_ceiling_nano_usd / NANO_PER_USD
      : DEFAULT_SPEND_LIMIT_USD;
    return (
      enabled() !== (limits.spend_ceiling_enabled === true) ||
      ceilingNumber() !== currentCeilingUsd ||
      warningNumber() / 100 !== (limits.spend_warning_ratio ?? SPEND_CEILING_WARN_RATIO) ||
      softStop() !== limits.spend_soft_stop
    );
  });

  const saveGuardrails = async () => {
    const current = data();
    const c = client();
    if (!current || !c || guardrailError()) return;
    const target = settingsQuery.capture();
    setSaveState("saving");
    setSaveError(undefined);
    try {
      const currentCeilingUsd = current.limits.session_spend_ceiling_nano_usd != null
        ? current.limits.session_spend_ceiling_nano_usd / NANO_PER_USD
        : DEFAULT_SPEND_LIMIT_USD;
      const targetCeilingUsd = Number.isFinite(ceilingNumber()) && ceilingNumber() >= 0
        ? ceilingNumber()
        : currentCeilingUsd;
      const limits = await c.updateLimitsSettings(
        changedLimitsFields(current.limits, {
          spend_ceiling_enabled: enabled(),
          session_spend_ceiling_nano_usd: Math.round(targetCeilingUsd * NANO_PER_USD),
          spend_warning_ratio: warnPercentValid()
            ? warningNumber() / 100
            : current.limits.spend_warning_ratio ?? SPEND_CEILING_WARN_RATIO,
          spend_soft_stop: softStop(),
        }),
        props.projectId,
      );
      target.update((latest) => ({ ...(latest ?? current), limits }));
      setSaveState("saved");
    } catch (error) {
      setSaveState("idle");
      setSaveError(error instanceof Error ? error.message : String(error));
    }
  };

  const exportCSV = async () => {
    const c = client();
    if (!c || exporting()) return;
    const projectId = props.projectId, projectName = props.projectName;
    setExporting(true); setExportError(undefined);
    try {
      const first = await c.getProjectCostReport(projectId, { limit: 200, sort: "id" });
      const sessions = [...first.sessions];
      let cursor = first.next_cursor;
      const seen = new Set<string>();
      while (cursor) {
        if (seen.has(cursor)) throw new Error("Cost export pagination did not advance.");
        seen.add(cursor);
        const next = await c.getProjectCostReport(projectId, { limit: 200, sort: "id", cursor });
        sessions.push(...next.sessions); cursor = next.next_cursor;
      }
      const text = buildProjectCostCSV({ ...first, sessions }, projectName);
      await downloadExport(new Blob([text], { type: "text/csv;charset=utf-8" }), costExportFilename(projectName, "csv"));
    } catch (error) {
      setExportError(error instanceof Error ? error.message : String(error));
    } finally { setExporting(false); }
  };

  const headerActions = (
    <div class="project-cost-actions">
      <DenButton variant="secondary" compact data-testid="project-cost-refresh" onClick={() => void refetch()}>
        Refresh
      </DenButton>
      <DenButton
        variant="secondary"
        compact
        data-testid="project-cost-export"
        disabled={!projectHasUsage() || exporting()}
        onClick={() => void exportCSV()}
      >
        {exporting() ? "Exporting…" : "Export CSV"}
      </DenButton>
    </div>
  );

  return (
    <section class="project-cost-view" data-testid="project-cost-view">
      <BrowseStagePanel
        appStore={props.appStore}
        back={props.back}
        scrollHost="main"
        title={<span>Cost</span>}
        primary={headerActions}
        meta={
          <Show when={summary()}>
            {(sum) => <span>Estimated usage · {sourceAsOfLabel(sum())}</span>}
          </Show>
        }
        error={exportError() ?? (costQuery.error() || settingsQuery.error() ? "Could not load project cost data. Try refreshing." : undefined)}
        errorTestId="project-cost-error"
        testId="project-cost-stage"
        requireClient={false}
        contentReady={() =>
          data() != null ||
          costQuery.error() != null ||
          settingsQuery.error() != null
        }
      >
        <div class="project-cost-content">
          <Show when={(costQuery.loading() || settingsQuery.loading()) && !data()}>
            <div class="project-cost-skeleton" data-testid="project-cost-loading" aria-label="Loading project cost">
              <div />
              <div />
              <div />
            </div>
          </Show>

          <ShowLatest when={data()}>
            {(loaded) => (
              <>
                <Show when={!loaded().pricing.cost_tracking_enabled}>
                  <div class="project-cost-notice" data-testid="project-cost-tracking-off">
                    <div>
                      <strong>Cost tracking is off</strong>
                      <span>Turn it on to record new project usage. Existing estimates remain visible.</span>
                    </div>
                    <Show when={props.onOpenCostSettings}>
                      <DenButton variant="primary" compact onClick={() => props.onOpenCostSettings?.()}>
                        Open Settings → Cost
                      </DenButton>
                    </Show>
                  </div>
                </Show>

                <ShowLatest when={summary()}>
                  {(sum) => (
                    <>
                      <div class="project-cost-intro">
                        <div>
                          <span class="project-cost-eyebrow">Project usage</span>
                          <h1>{props.projectName}</h1>
                          <p>Figures are estimates, not invoices. Archived sessions are included.</p>
                        </div>
                        <Show when={!priced(sum())}>
                          <span class="den-status-mark" data-tone="warning">Tokens only · pricing unavailable</span>
                        </Show>
                        <Show when={estimateView(sum()).coverage === "lower_bound"}>
                          <span class="den-status-mark" data-tone="warning" data-testid="project-cost-lower-bound-badge">
                            Lower bound · {estimateView(sum()).reasons.join(" · ")}
                          </span>
                        </Show>
                        <Show when={unreportedFreeCalls(sum()) > 0}>
                          <span class="den-status-mark" data-testid="project-cost-unreported-local-badge">
                            {callCount(unreportedFreeCalls(sum()), "local")} · no charge
                          </span>
                        </Show>
                      </div>

                      <div class="project-cost-kpis" data-testid="project-cost-kpis">
                        <article>
                          <span>Estimated spend</span>
                          <strong><CostAmount summary={sum()} unpricedText="—" testId="project-cost-estimated-spend" /></strong>
                          <CacheSavingsNote savings={sum().cache_savings} totals={sum().token_totals} />
                          <small data-tip={formatTokenSplitExact(sum().token_totals)}>{priced(sum()) ? `${formatTokenSplit(sum().token_totals)} · ${sourceAsOfLabel(sum())}` : "Pricing unavailable"}</small>
                        </article>
                        <article>
                          <span>Total tokens</span>
                          <strong data-tip={`${formatTokens(totalTokens(sum()))} tokens`}>{formatTokensCompact(totalTokens(sum()))}</strong>
                          <small>{formatTokenSplit(sum().token_totals)}</small>
                        </article>
                        <article>
                          <span>Sessions</span>
                          <strong>{loaded().report.sessions.length}</strong>
                          <small>{loaded().report.sessions.length - archivedSessions()} active · {archivedSessions()} archived</small>
                        </article>
                        <article>
                          <span>Worker activity</span>
                          <strong>{workerTasks()}</strong>
                          <small>{workerTasks() === 1 ? "worker task" : "worker tasks"} across project sessions</small>
                        </article>
                      </div>

                      <div class="project-cost-overview-grid">
                        <article class="project-cost-card project-cost-composition">
                          <header>
                            <div>
                              <span class="project-cost-eyebrow">Composition</span>
                              <h2>Where usage goes</h2>
                            </div>
                          </header>
                          <div class="project-cost-composition__panels">
                            <ProjectCostCompositionPanel
                              title="By estimated cost"
                              basis="usd"
                              summary={sum()}
                              roles={roles()}
                              empty={
                                !priced(sum())
                                  ? "Pricing unavailable."
                                  : "No priced spend to chart yet."
                              }
                            />
                            <ProjectCostCompositionPanel
                              title="By tokens"
                              basis="tokens"
                              summary={sum()}
                              roles={roles()}
                            />
                          </div>
                        </article>

                        <article class="project-cost-card project-cost-guardrails" data-testid="project-cost-guardrails">
                          <header>
                            <div>
                              <span class="project-cost-eyebrow">Session guardrails</span>
                              <h2>{allowsLanding() ? "Warn, then land" : "Warn, then close out"}</h2>
                            </div>
                            <span class="den-status-mark" data-tone={enabled() ? "positive" : undefined}>{enabled() ? "On" : "Off"}</span>
                          </header>
                          <p>Applies to every session in this project. {allowsLanding()
                            ? "At the limit, a running session can land its work before pausing."
                            : "At the limit, a running session closes out without another tool round."} This is a per-session limit.</p>
                          <p>Warnings and stops require priced usage. Unpriced usage is never presented as protected.</p>
                          <Show when={deviceStop()}>
                            {(amount) => (
                              <p class="project-cost-policy-note">
                                Device Settings requires a {formatUSD(amount())} stop. This project may warn earlier or set a lower stop, but cannot weaken it.
                              </p>
                            )}
                          </Show>
                          <DenCheckbox
                            checked={enabled()}
                            disabled={deviceStop() != null}
                            data-testid="project-cost-guardrail-enabled"
                            onChange={(event) => {
                              setEnabled(event.currentTarget.checked);
                              setSaveState("idle");
                            }}
                          >
                            Enable per-session spend limit
                          </DenCheckbox>
                          <SettingsGovernedGroup
                            label="Per-session spend limit"
                            active={enabled()}
                            inset={false}
                          >
                          <div class="project-cost-guardrails__fields">
                            <DenField label="Warn at">
                              <div class="project-cost-suffix-input">
                                <DenNumberInput
                                  min={WARN_PERCENT_MIN}
                                  max={WARN_PERCENT_MAX}
                                  step="1"
                                  value={warningPercent()}
                                  data-testid="project-cost-warning-percent"
                                  onInput={(event) => {
                                    setWarningPercent(event.currentTarget.value);
                                    setSaveState("idle");
                                  }}
                                />
                                <span class="project-cost-input-affix">%</span>
                              </div>
                            </DenField>
                            <DenField label="Spend limit">
                              <div class="project-cost-prefix-input">
                                <span class="project-cost-input-affix">$</span>
                                <DenNumberInput
                                  min="0.01"
                                  max={deviceStop() == null ? undefined : String(deviceStop())}
                                  step="0.01"
                                  value={ceiling()}
                                  data-testid="project-cost-ceiling-usd"
                                  onInput={(event) => {
                                    setCeiling(event.currentTarget.value);
                                    setSaveState("idle");
                                  }}
                                />
                              </div>
                            </DenField>
                          </div>
                          <DenCheckbox
                            checked={softStop()}
                            disabled={!deviceAllowsSoftStop()}
                            data-testid="project-cost-soft-stop"
                            onChange={(event) => {
                              setSoftStop(event.currentTarget.checked);
                              setSaveState("idle");
                            }}
                          >
                            Allow a soft landing at the limit
                          </DenCheckbox>
                          <p class="project-cost-policy-note">
                            On by default. One bounded round can finish work already in flight or return the best usable result. If it uses tools, a prose-only closeout follows. New prompts and workers remain blocked.
                          </p>
                          <Show when={!deviceAllowsSoftStop()}>
                            <p class="project-cost-policy-note">
                              Device Settings requires an immediate closeout, so this project cannot enable a soft landing.
                            </p>
                          </Show>
                          </SettingsGovernedGroup>
                          <Show when={enabled() && !guardrailError()}>
                            <div class="project-cost-guardrails__sequence" aria-label={`Warn at ${formatUSD(ceilingNumber() * warningNumber() / 100)} then ${allowsLanding() ? "land" : "close out"} at ${formatUSD(ceilingNumber())}`}>
                              <span>Start</span>
                              <i><b style={{ width: `${warningNumber()}%` }} /></i>
                              <span>Warn {formatUSD(ceilingNumber() * warningNumber() / 100)}</span>
                              <span>{allowsLanding() ? "Land" : "Close out"} {formatUSD(ceilingNumber())}</span>
                            </div>
                          </Show>
                          <Show when={guardrailError()}>
                            {(message) => <p class="project-cost-field-error" role="alert">{message()}</p>}
                          </Show>
                          <Show when={saveError()}>
                            {(message) => <p class="project-cost-field-error" role="alert">{message()}</p>}
                          </Show>
                          <footer>
                            <span role="status">
                              {saveState() === "saving" ? "Saving…" : saveState() === "saved" ? "Saved to this project" : guardrailDirty() ? "Unsaved changes" : "Project settings are up to date"}
                            </span>
                            <DenButton
                              variant="primary"
                              compact
                              data-testid="project-cost-save-guardrails"
                              disabled={!guardrailDirty() || Boolean(guardrailError()) || saveState() === "saving"}
                              onClick={() => void saveGuardrails()}
                            >
                              Save guardrails
                            </DenButton>
                          </footer>
                        </article>
                      </div>

                      <article class="project-cost-card project-cost-sessions" data-testid="project-cost-sessions">
                        <header>
                          <div>
                            <span class="project-cost-eyebrow">Session breakdown</span>
                            <h2>Cost by session</h2>
                          </div>
                          <div class="project-cost-session-controls">
                            <label>
                              <span class="sr-only">Filter sessions</span>
                              <DenInput
                                type="search"
                                placeholder="Filter sessions…"
                                value={query()}
                                data-testid="project-cost-filter"
                                onInput={(event) => setQuery(event.currentTarget.value)}
                              />
                            </label>
                            <label>
                              <span class="sr-only">Sort sessions</span>
                              <DenSelect
                                aria-label="Sort sessions"
                                value={sort()}
                                data-testid="project-cost-sort"
                                options={[
                                  { value: "cost", label: "Highest cost" },
                                  { value: "activity", label: "Recent activity" },
                                  { value: "tokens", label: "Most tokens" },
                                ]}
                                onValueChange={(value) => batch(() => { setSort(value as ProjectCostSort); resetPage(); })}
                              />
                            </label>
                          </div>
                        </header>

                        <Show
                          when={rows().length > 0 || Boolean(projectUtilityUsage()) || Boolean(retiredSessionUsage())}
                          fallback={
                            <div class="project-cost-empty">
                              <strong>{loaded().report.sessions.length === 0 ? "No usage recorded yet" : "No matching sessions"}</strong>
                              <span>{loaded().report.sessions.length === 0 ? "Session estimates will appear here after the first priced model call." : "Try a different session title."}</span>
                            </div>
                          }
                        >
                          <Scrollport class="project-cost-table-wrap" axis="x" contentClass="project-cost-table-wrap__content">
                            <table>
                              <thead>
                                <tr>
                                  <th>Session</th>
                                  <th>Usage</th>
                                  <th>Tokens</th>
                                  <th>Workers</th>
                                  <th>Last activity</th>
                                </tr>
                              </thead>
                              <tbody>
                                <Show when={projectUtilityUsage()} keyed>
                                  {(utility) => (
                                    <tr class="project-cost-utility-row" data-testid="project-cost-utilities-row">
                                      <td>
                                        <div class="project-cost-utility-name">
                                          <strong>Project utilities</strong>
                                          <span>Outside a session</span>
                                        </div>
                                      </td>
                                      <td>
                                        <div class="project-cost-session-amount">
                                          <strong>
                                            <CostAmount summary={utility} unpricedText="Unpriced" />
                                          </strong>
                                          <i aria-hidden="true"><b style={{ width: `${usageBarPercent(utility)}%` }} /></i>
                                        </div>
                                      </td>
                                      <td data-tip={formatTokenSplitExact(utility.token_totals)}>
                                        <strong>{formatTokensCompact(totalTokens(utility))}</strong>
                                        <span>{formatTokenSplit(utility.token_totals)}</span>
                                      </td>
                                      <td>
                                        <strong>—</strong>
                                        <span>not applicable</span>
                                      </td>
                                      <td>
                                        <strong>On demand</strong>
                                        <span>project context</span>
                                      </td>
                                    </tr>
                                  )}
                                </Show>
                                <Show when={retiredSessionUsage()} keyed>
                                  {(retired) => (
                                    <tr class="project-cost-utility-row" data-testid="project-cost-retired-row">
                                      <td>
                                        <div class="project-cost-utility-name">
                                          <strong>Deleted or expired sessions</strong>
                                          <span>Retained spend history</span>
                                        </div>
                                      </td>
                                      <td>
                                        <div class="project-cost-session-amount">
                                          <strong>
                                            <CostAmount summary={retired} unpricedText="Unpriced" />
                                          </strong>
                                          <i aria-hidden="true"><b style={{ width: `${usageBarPercent(retired)}%` }} /></i>
                                        </div>
                                      </td>
                                      <td data-tip={formatTokenSplitExact(retired.token_totals)}>
                                        <strong>{formatTokensCompact(totalTokens(retired))}</strong>
                                        <span>{formatTokenSplit(retired.token_totals)}</span>
                                      </td>
                                      <td>
                                        <strong>{retired.workers.task_count ?? 0}</strong>
                                        <span>retained tasks</span>
                                      </td>
                                      <td>
                                        <strong>Retained</strong>
                                        <span>session removed</span>
                                      </td>
                                    </tr>
                                  )}
                                </Show>
                                <For each={rows()}>
                                  {(row) => (
                                    <tr>
                                      <td>
                                        <button type="button" onClick={() => props.onOpenSession(row.session.id)}>
                                          <strong>{row.session.title?.trim() || "Untitled session"}</strong>
                                          <span>
                                            {row.session.archived_at ? "Archived" : row.session.status === "busy" ? "Running" : row.session.status}
                                            {row.session.pin_rank != null ? " · Pinned" : ""}
                                          </span>
                                        </button>
                                      </td>
                                      <td>
                                        <div class="project-cost-session-amount">
                                          <strong><CostAmount summary={row.cost} unpricedText="Unpriced" /></strong>
                                          <i aria-hidden="true"><b style={{ width: `${barPercent(row)}%` }} /></i>
                                        </div>
                                      </td>
                                      <td data-tip={formatTokenSplitExact(row.cost.token_totals)}>
                                        <strong>{formatTokensCompact(totalTokens(row.cost))}</strong>
                                        <span>{formatTokenSplit(row.cost.token_totals)}</span>
                                      </td>
                                      <td>
                                        <strong>{row.cost.workers.task_count ?? 0}</strong>
                                        <span>{row.cost.workers.task_count === 1 ? "task" : "tasks"}</span>
                                      </td>
                                      <td>
                                        <strong>{formatRelativeTime(Date.parse(row.session.activity_at))}</strong>
                                        <span data-tip={new Date(row.session.activity_at).toLocaleString()}>{new Date(row.session.activity_at).toLocaleDateString()}</span>
                                      </td>
                                    </tr>
                                  )}
                                </For>
                              </tbody>
                            </table>
                          </Scrollport>
                        </Show>
                        <TablePager page={page()} pageSize={COST_PAGE_SIZE} total={report()?.total ?? 0} canNext={!!report()?.next_cursor} loading={costQuery.loading()} onPageChange={showPage} testId="project-cost-pager" ariaLabel="Cost history pages" />
                        <p class="project-cost-sessions__note">
                          Project utilities are model calls made outside a session. Deleted or expired sessions retain an aggregate spend-history row. Every lane is included in the project total and export.
                        </p>
                      </article>
                    </>
                  )}
                </ShowLatest>
              </>
            )}
          </ShowLatest>
        </div>
      </BrowseStagePanel>
    </section>
  );
}
