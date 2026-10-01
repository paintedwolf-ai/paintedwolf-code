import { createResidentActivity } from "../../ui/resident-activity.ts";
import { subscribeSourceInvalidation } from "../../files/source/source-invalidation.ts";
import { createBoundedDebouncedAsyncScheduler } from "../../store/coalesced-async.ts";
import { SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS } from "../../files/source/source-refresh.ts";
import { loadProjectTrust, openProjectTrust, projectTrustView } from "../../settings/security/project-trust.ts";
import { showTrustReview } from "../trust/trust-review-navigation.ts";
import {
  Show,
  For,
  createEffect,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import type { CostStore } from "../../store/cost-store.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import {
  coordinatorModelStatus,
  modelChipLabel,
  modelRefLabel,
} from "../../chat/status/composer-status-model.ts";
import {
  navigateFromStatusChip,
  statusChipNavigationAvailable,
  type StatusChipNavigationTarget,
} from "../../chat/status/status-navigation-sink.ts";
import {
  APPROVAL_POSTURE_CARDS,
  postureLabel,
} from "../../settings/security/approvals-settings-copy.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { costTrackingEnabled, costTrackingSince } from "../../cost/cost-tracking.ts";
import { costChipView } from "../../cost/cost-format.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import {
  SPEND_CEILING_WARN_RATIO,
  spendCeilingApproaching,
  spendCeilingReadout,
} from "../../settings/budgets/spend-ceiling-readout.ts";
import { NANO_PER_USD } from "../../cost/nano-usd.ts";
import { trustDotState } from "../../settings/security/trust-model.ts";
import { TRUST_COPY } from "../../settings/security/trust-copy.ts";
import type { LycaonClient } from "../../api/client.ts";
import { TrustPopoverBody } from "../trust/TrustPopover.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { CostSummaryPanel } from "./CostSummaryPanel.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";

type PopoverId = "trust" | "model" | "posture" | "cost";

type Props = {
  appStore: AppStore;
  settingsStore: SettingsStore;
  costStore: CostStore;
  /** Empty while the project has no presented chat; only the cost cell follows it. */
  sessionId: string;
  projectId: string;
  roots?: readonly ProjectRoot[];
};

type ProjectSource = { client: LycaonClient; key: string; projectId: string };

function NavigationLink(props: {
  target: StatusChipNavigationTarget;
  testId: string;
  label: string;
  onNavigate?: () => void;
}) {
  return (
    <Show when={statusChipNavigationAvailable()}>
      <button
        type="button"
        class="den-status-popover__manage"
        data-testid={`status-popover-manage-${props.testId}`}
        onClick={() => {
          navigateFromStatusChip(props.target);
          props.onNavigate?.();
        }}
      >
        {props.label}
      </button>
    </Show>
  );
}

export function ComposerStatusChips(props: Props) {
  const residentLive = useResidentLive();
  const [open, setOpen] = createSignal<PopoverId | null>(null);
  let popoverEl: HTMLDivElement | undefined;
  const chipEls: Partial<Record<PopoverId, HTMLButtonElement>> = {};

  const client = () => {
    // Track connection state before resolving the client.
    props.appStore.state.sidecarStatus;
    return getLycaonClient();
  };

  const session = () => {
    const current = props.appStore.state.currentSession;
    return current?.id === props.sessionId ? current : undefined;
  };

  // Scope refreshes retain the displayed value while loading.
  const projectSource = (revision: () => number | string) => (): ProjectSource | null => {
    const c = client();
    const projectId = props.projectId.trim();
    if (!c || !projectId) return null;
    return { client: c, key: `${projectId}:${revision()}`, projectId };
  };

  const trustQuery = createSurfaceQuery({
    name: "project-trust",
    source: projectSource(() => JSON.stringify([
      props.appStore.state.projectTrustRevision,
      props.roots?.map(root => [root.id, root.path, root.label]),
    ])),
    scope: (source) => source.projectId,
    load: ({ client: c, projectId }) => loadProjectTrust(c, projectId),
    // Inventory can traverse a large repository; the chip renders its pending state.
    required: false,
  });
  createResidentActivity(() => {
    const refresh = createBoundedDebouncedAsyncScheduler(async () => { await trustQuery.refresh(); }, SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS);
    const stop = subscribeSourceInvalidation(scope => { if (scope.projectId === props.projectId) refresh.schedule(); });
    return () => { stop(); refresh.cancel(); };
  });
  const approvalsQuery = createSurfaceQuery({
    name: "project-approvals",
    source: projectSource(() => props.appStore.state.approvalsRevision),
    scope: (source) => source.projectId,
    load: ({ client: c, projectId }) => c.getApprovalsSettings(projectId),
  });
  const policyQuery = createSurfaceQuery({
    name: "model-policy",
    source: projectSource(() => props.appStore.state.modelPolicyRevision),
    scope: (source) => source.projectId,
    load: ({ client: c, projectId }) => c.getModelPolicySettings(projectId, true),
  });

  const policy = () => policyQuery.value() ?? null;
  const modelStatus = () => coordinatorModelStatus(session(), policy());

  const trust = () => projectTrustView(props.projectId).value;
  const trustState = (): "loading" | "ready" | "unavailable" => {
    if (!client()) return "unavailable";
    if (trust()) return "ready";
    if (projectTrustView(props.projectId).error) return "unavailable";
    return "loading";
  };
  const dot = () => {
    const t = trust();
    return t && trustState() === "ready" ? trustDotState(t) : null;
  };
  const [openingTrust, setOpeningTrust] = createSignal(false);
  const [markingAllTrusted, setMarkingAllTrusted] = createSignal(false);
  const [trustOpenError, setTrustOpenError] = createSignal<string | null>(null);
  const openTrust = async () => {
    const c = client();
    if (!c || openingTrust() || markingAllTrusted()) return;
    const projectId = props.projectId;
    setOpeningTrust(true); setTrustOpenError(null);
    try {
      await openProjectTrust(c, projectId);
      if (props.projectId === projectId) {
        showTrustReview(projectId);
        setOpen(null);
        void trustQuery.refresh();
      }
    } catch (err) { setTrustOpenError(err instanceof Error ? err.message : "Could not open trust changes."); }
    finally { setOpeningTrust(false); }
  };
  const markAllTrusted = async () => {
    const c = client();
    if (!c || markingAllTrusted() || openingTrust()) return;
    if ((trust()?.unread_count ?? 0) === 0) return;
    const projectId = props.projectId;
    setMarkingAllTrusted(true); setTrustOpenError(null);
    try {
      await openProjectTrust(c, projectId);
      if (props.projectId === projectId) {
        props.appStore.actions.bumpProjectTrustRevision();
        void trustQuery.refresh();
      }
    } catch (err) { setTrustOpenError(err instanceof Error ? err.message : "Could not mark all trusted."); }
    finally { setMarkingAllTrusted(false); }
  };
  const trustLabel = () => {
    const t = trust();
    if (trustState() === "unavailable") return TRUST_COPY.chipUnavailable;
    if (!t) return TRUST_COPY.chipLoading;
    return TRUST_COPY.chipLabel(t.review.changes.length);
  };

  const approvals = () => approvalsQuery.value() ?? null;
  const posture = () => approvals()?.approval_posture ?? null;
  const approvalsOff = () => approvals()?.never_ask === true;
  const approvalsLabel = () =>
    approvalsOff() ? "Off" : postureLabel(posture() ?? undefined);
  const postureFromProject = () =>
    approvals()?.field_sources?.approval_posture === "override";
  const approvalsRestoredByProject = () =>
    approvals()?.field_sources?.never_ask === "override" && !approvalsOff();
  const postureCard = () =>
    APPROVAL_POSTURE_CARDS.find((card) => card.id === posture());

  const trackingOn = () => costTrackingEnabled(props.settingsStore);
  // Connect hydrates pricing; a failed hydrate ends the connect's loading state.
  usePresentationParticipant(
    "cost-tracking-setting",
    () =>
      !client() ||
      props.settingsStore.state.pricing !== undefined ||
      !props.appStore.state.isLoading,
  );
  const globalLimits = () => props.settingsStore.state.limits;
  const projectLimits = () => {
    const effective = props.settingsStore.state.effectiveLimits;
    return effective?.projectId === props.projectId
      ? effective.limits
      : undefined;
  };
  const limits = () => projectLimits() ?? globalLimits();
  const ceilingState = () => {
    const l = limits();
    if (!l) return undefined;
    return {
      enabled: l.spend_ceiling_enabled === true,
      ceilingUsd: (l.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD,
      warningRatio: l.spend_warning_ratio ?? SPEND_CEILING_WARN_RATIO,
    };
  };
  const boardCost = () => {
    const summary = props.appStore.state.board?.cost;
    return summary?.session_id === props.sessionId ? summary : undefined;
  };
  const costSummary = () => props.costStore.state.session ?? boardCost();
  const costView = () =>
    costChipView(props.costStore.state.session, boardCost(), ceilingState());

  // Load limits before showing the ceiling suffix.
  const [limitsSettled, setLimitsSettled] = createSignal(false);
  createEffect(() => {
    if (!residentLive()) return;
    const c = client();
    const projectId = props.projectId;
    if (!trackingOn() || !c || !projectId) return;
    let current = true;
    const device =
      untrack(globalLimits) != null
        ? Promise.resolve()
        : c.getLimitsSettings().then((l) => {
            if (current && props.settingsStore.state.limits == null) {
              props.settingsStore.actions.setLimits(l);
            }
          });
    const project = c.getLimitsSettings(projectId).then((l) => {
      if (current) props.settingsStore.actions.setEffectiveLimits(projectId, l);
    });
    void Promise.allSettled([device, project]).then(() => {
      if (current) setLimitsSettled(true);
    });
    onCleanup(() => {
      current = false;
    });
  });
  usePresentationParticipant(
    "session-limits",
    () => !trackingOn() || !client() || limitsSettled(),
  );

  createEffect(() => {
    if (!residentLive()) return;
    const sid = props.sessionId;
    const on = trackingOn();
    const c = client();
    if (!on || !sid || !c) {
      props.costStore.actions.clear();
      return;
    }
    void props.costStore.refreshSession(c, sid);
  });
  usePresentationParticipant("session-cost", () => {
    const sid = props.sessionId.trim();
    if (!trackingOn() || !client() || !sid) return true;
    const cost = props.costStore.state;
    // A refresh of a loaded summary keeps the chip presentable.
    return cost.sessionId === sid && (cost.session !== undefined || !cost.loading);
  });

  // Open cost popovers use the live refresh cadence.
  createEffect(() => {
    if (!residentLive()) return;
    if (open() !== "cost") return;
    const sid = props.sessionId;
    const c = client();
    if (!trackingOn() || !sid || !c) return;
    void props.costStore.refreshSession(c, sid);
    onCleanup(props.costStore.holdLiveRefresh());
  });

  const ceilingReadout = () =>
    spendCeilingReadout({
      enabled: ceilingState()?.enabled === true,
      ceilingUsd: ceilingState()?.ceilingUsd ?? 0,
      warningRatio:
        ceilingState()?.warningRatio ?? SPEND_CEILING_WARN_RATIO,
      summary: costSummary(),
    });

  const approaching = () => spendCeilingApproaching(ceilingReadout());

  // Close popovers when the session changes.
  createEffect(() => {
    props.sessionId;
    setOpen(null);
  });

  createEffect(() => {
    if (!residentLive()) setOpen(null);
  });

  createAnchoredPopoverFocus(
    () => open() != null,
    () => popoverEl,
    {
      trigger: () => {
        const id = open();
        return id ? chipEls[id] : undefined;
      },
      onEscape: () => setOpen(null),
    },
  );

  const toggle = (id: PopoverId) => setOpen((cur) => (cur === id ? null : id));

  return (
    <div class="den-status-chips" data-testid="composer-status-chips">
      <button
        type="button"
        class="den-status-chip"
        data-cell="trust"
        data-testid="status-chip-trust"
        data-state={dot() ?? trustState()}
        aria-busy={trustState() === "loading" ? true : undefined}
        aria-haspopup="dialog"
        aria-expanded={open() === "trust"}
        ref={(el) => {
          chipEls.trust = el;
        }}
        onClick={() => toggle("trust")}
      >
        <span class="den-status-chip__dot" aria-hidden="true" />
        <span class="den-status-chip__label">{trustLabel()}</span>
      </button>
      <Show when={approvals()}>
        {(_cfg) => (
          <button
            type="button"
            class="den-status-chip"
            data-cell="posture"
            data-testid="status-chip-posture"
            data-posture={approvalsOff() ? "off" : posture() ?? undefined}
            aria-haspopup="dialog"
            aria-expanded={open() === "posture"}
            ref={(el) => {
              chipEls.posture = el;
            }}
            onClick={() => toggle("posture")}
          >
            <span class="den-status-chip__label">{approvalsLabel()}</span>
          </button>
        )}
      </Show>
      <Show when={modelStatus()}>
        {(status) => (
          <button
            type="button"
            class="den-status-chip"
            data-cell="model"
            data-testid="status-chip-model"
            aria-haspopup="dialog"
            aria-expanded={open() === "model"}
            ref={(el) => {
              chipEls.model = el;
            }}
            onClick={() => toggle("model")}
          >
            <span class="den-status-chip__label">{modelChipLabel(status())}</span>
          </button>
        )}
      </Show>
      <Show when={trackingOn()}>
        <button
          type="button"
          class="den-status-chip"
          classList={{
            "den-status-chip--warn": approaching(),
          }}
          data-cell="cost"
          data-testid="status-chip-cost"
          data-coverage={costView()?.coverage}
          data-ceiling-ratio={costView()?.ceiling?.ratio.toFixed(2)}
          aria-haspopup="dialog"
          aria-expanded={open() === "cost"}
          ref={(el) => {
            chipEls.cost = el;
          }}
          onClick={() => toggle("cost")}
        >
          {/* The hollow dot marks incomplete pricing. */}
          <Show when={costView()?.coverage === "lower_bound"}>
            <span class="den-status-chip__dot" role="img" aria-label="Lower bound" />
          </Show>
          <span class="den-status-chip__label" aria-label={costView()?.label ? undefined : "Estimated cost unavailable"}>{costView()?.label ?? ""}</span>
        </button>
      </Show>
      <Show when={open() === "trust"}>
          <AnchoredSurface
            class="den-status-popover den-status-popover--trust"
            role="dialog"
            ariaLabel="Project trust"
            testId="status-popover-trust"
            height="viewport"
            overflow="hidden"
            anchor={() => chipEls.trust}
            preferredSide="right"
            align="start"
            gap={6}
            onDismiss={() => setOpen(null)}
            ref={(el) => {
              popoverEl = el;
            }}
          >
            <Show when={trustState() === "ready" && trust()} keyed>
              {(value) => <TrustPopoverBody trust={value} onOpen={() => void openTrust()} opening={openingTrust()} />}
            </Show>
            <Show when={trustState() !== "ready"}>
              <p class="den-status-popover__title" {...chromeProps()}>{trustLabel()}</p>
              <p class="den-status-popover__hint">
                {trustState() === "unavailable"
                  ? TRUST_COPY.loadError
                  : TRUST_COPY.popoverLoading}
              </p>
            </Show>
            <Show when={trustOpenError()}>{message => <p class="den-settings-warn trust-popover__error" role="alert" data-tip={message()}>
              {message()}
            </p>}</Show>
            <button type="button" class="den-status-popover__manage" data-testid="trust-open-review" disabled={!client() || openingTrust() || markingAllTrusted()}
              onClick={() => void openTrust()}>{openingTrust() ? "Opening…" : "Open trust"}</button>
            <NavigationLink
              target={{ kind: "project-configuration", section: "trust" }}
              testId="trust"
              label="Trust settings"
              onNavigate={() => setOpen(null)}
            />
            <button type="button" class="den-status-popover__manage" data-testid="trust-mark-all-trusted" disabled={!client() || markingAllTrusted() || openingTrust()}
              onClick={() => void markAllTrusted()}>{markingAllTrusted() ? "Marking trusted…" : "Mark all trusted"}</button>
          </AnchoredSurface>
      </Show>

      <Show when={open() === "model"}>
          <AnchoredSurface
            class="den-status-popover"
            role="dialog"
            ariaLabel="Models in use"
            testId="status-popover-model"
            anchor={() => chipEls.model}
            preferredSide="right"
            align="start"
            gap={6}
            onDismiss={() => setOpen(null)}
            ref={(el) => {
              popoverEl = el;
            }}
          >
            <Show when={modelStatus()}>
              {(status) => (
                <>
                  <dl class="den-status-popover__grid">
                    <div>
                      <dt>{MODELS_SETTINGS_COPY.coordinatorSlotLabel}</dt>
                      <dd data-testid="status-popover-coordinator">
                        {modelRefLabel(status().ref)}
                        <Show when={status().sessionOverride}>
                          <span class="den-status-popover__note">
                            {" "}
                            · session override
                          </span>
                        </Show>
                      </dd>
                    </div>
                    <Show when={policy()}>
                      {(policy) => (
                        <>
                          <div>
                            <dt>{MODELS_SETTINGS_COPY.liteSlotLabel}</dt>
                            <dd>{modelRefLabel(policy().lite)}</dd>
                          </div>
                          <div>
                            <dt>Agent pool</dt>
                            <dd data-testid="status-popover-agent-pool">
                              <Show
                                when={policy().agent_pool.models.length > 0}
                                fallback="—"
                              >
                                <For each={policy().agent_pool.models}>
                                  {(ref, i) => (
                                    <>
                                      {i() > 0 ? ", " : ""}
                                      {modelRefLabel(ref)}
                                    </>
                                  )}
                                </For>
                              </Show>
                            </dd>
                          </div>
                        </>
                      )}
                    </Show>
                  </dl>
                  <NavigationLink
                    target={{ kind: "settings", section: "providers" }}
                    testId="providers"
                    label="Manage models in Settings"
                    onNavigate={() => setOpen(null)}
                  />
                </>
              )}
            </Show>
          </AnchoredSurface>
      </Show>

      <Show when={open() === "posture"}>
          <AnchoredSurface
            class="den-status-popover"
            role="dialog"
            ariaLabel="Approval level"
            testId="status-popover-posture"
            anchor={() => chipEls.posture}
            preferredSide="right"
            align="start"
            gap={6}
            onDismiss={() => setOpen(null)}
            ref={(el) => {
              popoverEl = el;
            }}
          >
            <p class="den-status-popover__title" {...chromeProps()}>
              {approvalsOff()
                ? "Approvals: off"
                : `Approval level: ${approvalsLabel()}`}
            </p>
            <Show
              when={!approvalsOff()}
              fallback={
                <p class="den-status-popover__hint">
                  The advanced disable switch is active. Your {postureLabel(posture() ?? undefined)} level remains saved but is not applying.
                </p>
              }
            >
              <Show when={postureCard()}>
              {(card) => <p class="den-status-popover__hint">{card().tagline}</p>}
              </Show>
            </Show>
            <p class="den-status-popover__hint" data-testid="status-popover-posture-source">
              {approvalsOff()
                ? "Disabled in device advanced settings."
                : approvalsRestoredByProject()
                ? "Approvals restored by this project; level follows device Settings."
                : postureFromProject()
                ? "Set by this project's override."
                : "Following device Settings."}
            </p>
            <NavigationLink
              target={{ kind: "settings", section: "approvals" }}
              testId="approvals"
              label="Manage approvals in Settings"
              onNavigate={() => setOpen(null)}
            />
          </AnchoredSurface>
      </Show>

      <Show when={open() === "cost"}>
          <AnchoredSurface
            class="den-status-popover den-status-popover--cost"
            role="dialog"
            ariaLabel="Estimated cost"
            testId="status-popover-cost"
            anchor={() => chipEls.cost}
            preferredSide="right"
            align="start"
            gap={6}
            height="content"
            onDismiss={() => setOpen(null)}
            ref={(el) => {
              popoverEl = el;
            }}
          >
            <CostSummaryPanel
              summary={costSummary()}
              trackingSince={costTrackingSince(props.settingsStore)}
              loading={props.costStore.state.loading}
            />
            <Show when={approaching()}>
              <p
                class="den-status-popover__hint"
                data-testid="status-popover-ceiling-warn"
              >
                Approaching this project's session spend ceiling.
              </p>
            </Show>
            <NavigationLink
              target={{ kind: "project-context", stage: "cost" }}
              testId="cost"
              label="View project cost"
              onNavigate={() => setOpen(null)}
            />
          </AnchoredSurface>
      </Show>
    </div>
  );
}
