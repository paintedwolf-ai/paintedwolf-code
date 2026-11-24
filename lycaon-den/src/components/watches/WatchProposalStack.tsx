import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { isApiErrorCode } from "../../api/http.ts";
import type { AgentWatch, StandingWatch } from "../../api/types.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import {
  DEFAULT_DAILY_RUN_CAP,
  DEFAULT_RUN_SPEND_LIMIT_NANO_USD,
  formatFirstPromptLine,
  formatHostWatchError,
  formatNanoUsdShort,
  formatWatchConditionsSummary,
  viewerTimeZone,
} from "../../watches/watch-format.ts";
import {
  computeDraftChangedFields,
  convertAgentConditionForStanding,
} from "../../watches/watch-schedule-preview.ts";
import {
  cancelSessionWatchAction,
  keepSessionWatchAction,
} from "../../watches/watches-actions.ts";
import {
  ensureProjectWatchesHydrated,
  ensureSessionWatchesHydrated,
  hideProposalOverflow,
  isProposalOverflowHidden,
  projectWatchesFor,
  refreshProjectWatches,
  sessionWatchesFor,
} from "../../watches/watches-store.ts";
import type { SlotContent } from "../../ui/slot-content.ts";
import { SystemNudge } from "../SystemNudge.tsx";

type Props = {
  sessionId: string;
  projectId: string;
  client: () => LycaonClient | null;
  announceLive?: boolean;
};

function hasSessionWatchProposals(sessionId: string): boolean {
  return sessionWatchesFor(sessionId).proposals.length > 0;
}

export function createWatchProposalSlot(args: {
  sessionId: () => string;
  projectId: () => string;
  client: () => LycaonClient | null;
  announceLive: () => boolean;
}): SlotContent {
  createEffect(() => {
    const client = args.client();
    const sid = args.sessionId().trim();
    const pid = args.projectId().trim();
    if (!client) return;
    if (sid) ensureSessionWatchesHydrated(client, sid);
    if (pid) ensureProjectWatchesHydrated(client, pid);
  });
  return {
    present: () => hasSessionWatchProposals(args.sessionId()),
    children: () => (
      <WatchProposalStack
        sessionId={args.sessionId()}
        projectId={args.projectId()}
        client={args.client}
        announceLive={args.announceLive()}
      />
    ),
  };
}

function formatOfferExpiry(iso: string): string {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return iso;
  return viewerCalendarTimeFormat().fullDate(ms);
}

/** Contextual proposal card stack in the chat top dock `proposal` slot. */
function WatchProposalStack(props: Props) {
  const [busyHandles, setBusyHandles] = createSignal<ReadonlySet<string>>(
    new Set(),
  );
  const [conflictHandles, setConflictHandles] = createSignal<
    ReadonlySet<string>
  >(new Set());
  const [errorsByHandle, setErrorsByHandle] = createSignal<
    ReadonlyMap<string, string>
  >(new Map());
  const renderedRevisions = new Map<string, number>();

  createEffect(() => {
    const client = props.client();
    const sid = props.sessionId.trim();
    const pid = props.projectId.trim();
    if (!client) return;
    if (sid) ensureSessionWatchesHydrated(client, sid);
    if (pid) ensureProjectWatchesHydrated(client, pid);
  });

  const proposals = createMemo(
    () => sessionWatchesFor(props.sessionId).proposals,
  );
  const standingById = createMemo(
    () => projectWatchesFor(props.projectId).standingById,
  );

  const visibleProposals = createMemo(() => {
    const all = proposals();
    return all.length >= 4 ? all.slice(0, 2) : all;
  });

  const overflowCount = createMemo(() => {
    const all = proposals();
    if (all.length < 4) return 0;
    if (isProposalOverflowHidden(props.sessionId, all)) return 0;
    return all.length - 2;
  });

  const setBusy = (handle: string, active: boolean) => {
    setBusyHandles((prev) => {
      const next = new Set(prev);
      if (active) next.add(handle);
      else next.delete(handle);
      return next;
    });
  };

  const setCardError = (handle: string, message: string | null) => {
    setErrorsByHandle((prev) => {
      const next = new Map(prev);
      if (message) next.set(handle, message);
      else next.delete(handle);
      return next;
    });
  };

  const markConflict = (handle: string) => {
    setConflictHandles((prev) => {
      const next = new Set(prev);
      next.add(handle);
      return next;
    });
  };

  const handleKeep = async (
    watch: AgentWatch,
    replaced: StandingWatch | undefined,
  ) => {
    const client = props.client();
    if (!client || busyHandles().has(watch.handle)) return;
    const draft = watch.standing_draft;
    if (!draft) return;

    setBusy(watch.handle, true);
    setCardError(watch.handle, null);
    try {
      const rev = draft.replaces
        ? (renderedRevisions.get(watch.handle) ?? replaced?.revision ?? 1)
        : undefined;
      await keepSessionWatchAction(
        client,
        props.sessionId,
        watch.handle,
        rev !== undefined ? { revision: rev } : {},
      );
    } catch (err) {
      if (isApiErrorCode(err, ["watch_revision_conflict"])) {
        markConflict(watch.handle);
        void refreshProjectWatches(client, props.projectId);
      } else {
        setCardError(watch.handle, formatHostWatchError(err));
      }
    } finally {
      setBusy(watch.handle, false);
    }
  };

  const handleDismiss = async (watch: AgentWatch) => {
    const client = props.client();
    if (!client || busyHandles().has(watch.handle)) return;
    setBusy(watch.handle, true);
    setCardError(watch.handle, null);
    try {
      await cancelSessionWatchAction(client, props.sessionId, watch.handle);
    } catch (err) {
      setCardError(watch.handle, formatHostWatchError(err));
    } finally {
      setBusy(watch.handle, false);
    }
  };

  const ariaLive = () =>
    props.announceLive === false ? ("off" as const) : ("polite" as const);
  const defaultSpendText = `Up to ${formatNanoUsdShort(
    DEFAULT_DAILY_RUN_CAP * DEFAULT_RUN_SPEND_LIMIT_NANO_USD,
  )} a day`;

  return (
    <div
      class="flex flex-col gap-den-2"
      data-testid="watch-proposal-stack"
    >
      <For each={visibleProposals()}>
        {(watch) => {
          const draft = watch.standing_draft;
          if (!draft) return null;
          const replaced = () =>
            draft.replaces
              ? standingById().get(draft.replaces)
              : undefined;
          createEffect(() => {
            const rep = replaced();
            if (rep && !renderedRevisions.has(watch.handle)) {
              renderedRevisions.set(watch.handle, rep.revision);
            }
          });
          const tz = () => replaced()?.time_zone ?? viewerTimeZone();
          const converted = () => {
            const baseMs = Date.parse(watch.created_at) || Date.now();
            return watch.conditions.map((c) =>
              convertAgentConditionForStanding(c, {
                repeat: watch.repeat,
                baseMs,
                timeZone: tz(),
              }),
            );
          };
          const whenLine = () =>
            `${formatWatchConditionsSummary(converted(), {
              timeZone: tz(),
              repeat: true,
            })} · New chat each run · ${defaultSpendText}`;
          const changedFieldsLine = () => {
            const rep = replaced();
            if (!draft.replaces) return null;
            if (!rep) return "Updates existing standing watch";
            const fields = computeDraftChangedFields(draft, converted(), rep);
            const suffix =
              fields.length > 0 ? `: ${fields.join(", ")}` : "";
            return `Changes ${rep.name}${suffix}`;
          };
          const hasConflict = () => conflictHandles().has(watch.handle);
          const isBusy = () => busyHandles().has(watch.handle);
          const errText = () => errorsByHandle().get(watch.handle) ?? null;
          const firstOverlap = () => watch.overlapping_standing?.[0];

          return (
            <SystemNudge
              role="status"
              ariaLive={ariaLive()}
              testId={`watch-proposal-card-${watch.handle}`}
              titleTestId={`watch-proposal-title-${watch.handle}`}
              title={`Keep ${draft.name} as a standing watch?`}
              dismissLabel="Dismiss proposal"
              dismissDisabled={isBusy()}
              dismissTestId={`watch-proposal-dismiss-${watch.handle}`}
              onDismiss={() => void handleDismiss(watch)}
              primaryAction={
                hasConflict()
                  ? {
                      label: "Review changes",
                      disabled: isBusy(),
                      testId: `watch-proposal-review-${watch.handle}`,
                      onClick: () =>
                        openRunningStage({
                          projectId: props.projectId,
                          prefillFromWatch: {
                            sessionId: props.sessionId,
                            handle: watch.handle,
                            reviewChanges: true,
                          },
                        }),
                    }
                  : {
                      label: "Keep",
                      disabled: isBusy(),
                      testId: `watch-proposal-keep-${watch.handle}`,
                      onClick: () => handleKeep(watch, replaced()),
                    }
              }
              secondaryAction={{
                label: "Edit and keep",
                disabled: isBusy(),
                testId: `watch-proposal-edit-${watch.handle}`,
                onClick: () =>
                  openRunningStage({
                    projectId: props.projectId,
                    prefillFromWatch: {
                      sessionId: props.sessionId,
                      handle: watch.handle,
                    },
                  }),
              }}
              description={
                <div class="flex flex-col gap-den-0-5">
                  <p data-testid={`watch-proposal-when-${watch.handle}`}>
                    {whenLine()}
                  </p>
                  <p data-testid={`watch-proposal-prompt-${watch.handle}`}>
                    {formatFirstPromptLine(draft.prompt)}
                  </p>
                  <Show
                    when={hasConflict()}
                    fallback={
                      <Show when={changedFieldsLine()}>
                        {(line) => (
                          <p
                            data-testid={`watch-proposal-replaces-${watch.handle}`}
                          >
                            {line()}
                          </p>
                        )}
                      </Show>
                    }
                  >
                    <p
                      class="text-den-warn"
                      data-testid={`watch-proposal-conflict-${watch.handle}`}
                    >
                      This watch changed since the proposal
                    </p>
                  </Show>
                  <Show when={firstOverlap()} keyed>
                    {(overlap) => (
                      <p data-testid={`watch-proposal-overlap-${watch.handle}`}>
                        Similar to{" "}
                        <button
                          type="button"
                          class="den-chat-content-link"
                          onClick={() =>
                            openRunningStage({
                              projectId: props.projectId,
                              watchId: overlap.watch_id,
                            })
                          }
                        >
                          {overlap.name}
                        </button>
                      </p>
                    )}
                  </Show>
                  <p data-testid={`watch-proposal-expiry-${watch.handle}`}>
                    Offer ends {formatOfferExpiry(watch.expires_at)}
                  </p>
                  <Show when={errText()}>
                    {(msg) => (
                      <p
                        class="text-den-caption text-den-err"
                        data-testid={`watch-proposal-error-${watch.handle}`}
                      >
                        {msg()}
                      </p>
                    )}
                  </Show>
                </div>
              }
            />
          );
        }}
      </For>

      <Show when={overflowCount() > 0}>
        <SystemNudge
          role="status"
          ariaLive={ariaLive()}
          testId="watch-proposal-overflow-card"
          title={`${overflowCount()} more proposals`}
          primaryAction={{
            label: "Review in Activity",
            testId: "watch-proposal-overflow-review",
            onClick: () =>
              openRunningStage({
                projectId: props.projectId,
                sessionId: props.sessionId,
              }),
          }}
          dismissLabel="Hide"
          dismissTestId="watch-proposal-overflow-hide"
          onDismiss={() => hideProposalOverflow(props.sessionId, proposals())}
        />
      </Show>
    </div>
  );
}
