import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import type { AppNotice } from "../notices/notice-model.ts";
import { noticeActions } from "../notices/notice-actions.ts";
import { SPEND_CEILING_NOTICE_CODE } from "../settings/budgets/spend-ceiling-readout.ts";
import { DenButton } from "./primitives/DenButton.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { scrollportMotionForViewport } from "../platform/scrolling/scrollport-motion.ts";
import { chromeProps, proseProps } from "../styling/ui-chrome.ts";

type Props = {
  notices: readonly AppNotice[];
  onDismiss: (id: string) => void;
  /**
   * When false, the composer activity cell controls live announcements during an active turn.
   */
  announceLive?: boolean;
};

/** An info line reports something already finished, so it clears itself. */
const INFO_AUTO_DISMISS_MS = 6_000;

function severityLabel(severity: AppNotice["severity"]): string {
  switch (severity) {
    case "warning":
      return "Warning";
    case "info":
      return "";
    default:
      return "Error";
  }
}

function summaryLine(notice: AppNotice): string {
  const hint = notice.suggestedAction?.trim();
  if (hint) return hint;
  return notice.message.trim();
}

function actionsFor(notice: AppNotice) {
  return notice.interaction?.actions ?? noticeActions(notice).map((action) => ({ ...action, dismiss: true }));
}

/** Inline status rail — document flow, never modal or fixed overlay. */
export function NoticeRail(props: Props) {
  // SpendCeilingReachedNudge presents the spend-ceiling stop.
  const notices = createMemo(() =>
    props.notices.filter((n) => n.code?.trim() !== SPEND_CEILING_NOTICE_CODE),
  );

  const [expandedIds, setExpandedIds] = createSignal<ReadonlySet<string>>(
    new Set(),
  );

  /** Stabilizes effects against fresh arrays with unchanged membership. */
  const noticeIds = createMemo(() =>
    notices()
      .map((n) => n.id)
      .join("|"),
  );

  /** Ids already offered their one auto-expand, so nothing re-opens a closed row. */
  const seenIds = new Set<string>();

  // New notices open without closing another row.
  createEffect(() => {
    noticeIds();
    const rows = untrack(notices);
    const live = new Set(rows.map((n) => n.id));
    const latest = rows[rows.length - 1]?.id;
    for (const id of [...seenIds]) if (!live.has(id)) seenIds.delete(id);
    setExpandedIds((prev) => {
      const next = new Set([...prev].filter((id) => live.has(id)));
      if (latest && !seenIds.has(latest)) next.add(latest);
      return next;
    });
    for (const id of live) seenIds.add(id);
  });

  // Membership changes do not restart an existing expiry timer.
  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  createEffect(() => {
    const rows = notices();
    const expiring = new Set(rows.filter((notice) => notice.severity === "info" && notice.interaction?.autoDismiss !== false).map((notice) => notice.id));
    for (const [id, timer] of timers) {
      if (expiring.has(id)) continue;
      clearTimeout(timer);
      timers.delete(id);
    }
    for (const id of expiring) {
      if (timers.has(id)) continue;
      timers.set(id, setTimeout(() => {
        timers.delete(id);
        props.onDismiss(id);
      }, INFO_AUTO_DISMISS_MS));
    }
  });
  onCleanup(() => {
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
  });

  /** The list is capped: a notice that grows past the fold brings its top up. */
  const revealNotice = (row: HTMLLIElement) => {
    const list = row.closest<HTMLElement>(".den-notice-rail-list > .den-scrollport__viewport");
    if (!list) return;
    requestAnimationFrame(() => {
      const rowBox = row.getBoundingClientRect();
      const listBox = list.getBoundingClientRect();
      const overflowsBelow = rowBox.bottom > listBox.bottom;
      if (!overflowsBelow) return;
      const top = list.scrollTop + (rowBox.top - listBox.top);
      const motion = scrollportMotionForViewport(list);
      if (motion) void motion.revealOffset(top);
      else list.scrollTop = top;
    });
  };

  const toggleExpanded = (id: string, row: HTMLLIElement) => {
    let opened = false;
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else {
        next.add(id);
        opened = true;
      }
      return next;
    });
    if (opened) revealNotice(row);
  };

  const isExpanded = (id: string) => expandedIds().has(id);

  const announceLive = () => props.announceLive !== false;

  return (
    <Show when={notices().length > 0}>
      <section
        class="den-notice-rail"
        data-testid="notice-rail"
        role="status"
        aria-label="Status messages"
        aria-live={announceLive() ? "polite" : "off"}
        aria-relevant="additions"
      >
        <Scrollport class="den-notice-rail-list" contentAs="ul" contentClass="den-notice-rail-list__content">
          <For each={notices()}>
            {(notice) => {
              let row!: HTMLLIElement;
              return (
              // Rows are not alerts — the rail's polite region controls announcements.
              <li
                ref={row}
                data-testid="notice"
                data-notice-id={notice.id}
                data-notice-code={notice.code}
              >
                <div
                  class="den-notice"
                  classList={{
                    "den-notice--error": notice.severity === "error",
                    "den-notice--warning": notice.severity === "warning",
                    "den-notice--info": notice.severity === "info",
                  }}
                >
                  {/* Header chrome would block selecting the notice text. */}
                  <div class="den-notice-body" {...proseProps()}>
                    <div class="den-notice-summary">
                      <p class="den-notice-title">
                        <Show when={severityLabel(notice.severity)} keyed>
                          {(label) => <span class="den-notice-kind">{label}</span>}
                        </Show>
                        {notice.title}
                      </p>
                      <Show when={!isExpanded(notice.id)}>
                        <p class="den-notice-teaser">{summaryLine(notice)}</p>
                      </Show>
                    </div>
                    <Show when={isExpanded(notice.id)}>
                      <p class="den-notice-message">{notice.message}</p>
                      <Show when={notice.suggestedAction} keyed>
                        {(action) => (
                          <p class="den-notice-action">{action}</p>
                        )}
                      </Show>
                      <Show when={actionsFor(notice).length > 0}>
                        <div class="den-notice-actions">
                          <For each={actionsFor(notice)}>
                            {(action) => (
                              <DenButton
                                variant={action.variant ?? "secondary"}
                                compact
                                data-testid="notice-action"
                                {...chromeProps()}
                                onClick={() => {
                                  action.run?.();
                                  if (action.dismiss !== false) props.onDismiss(notice.id);
                                }}
                              >
                                {action.label}
                              </DenButton>
                            )}
                          </For>
                        </div>
                      </Show>
                    </Show>
                  </div>
                  <div class="den-notice-controls">
                    <button
                      type="button"
                      class="den-notice-toggle"
                      aria-expanded={isExpanded(notice.id)}
                      aria-label={
                        isExpanded(notice.id) ? "Hide details" : "Show details"
                      }
                      onClick={() => toggleExpanded(notice.id, row)}
                    >
                      {/* A narrow rail swaps the word for the caret. */}
                      <span class="den-notice-toggle-label">
                        {isExpanded(notice.id) ? "Less" : "Details"}
                      </span>
                      <span
                        class="den-notice-toggle-glyph den-tool-chicklet-caret"
                        aria-hidden="true"
                      />
                    </button>
                    <button
                      type="button"
                      class="den-notice-dismiss den-inset-icon-btn"
                      aria-label="Dismiss"
                      onClick={() => props.onDismiss(notice.id)}
                    >
                      ×
                    </button>
                  </div>
                </div>
              </li>
              );
            }}
          </For>
        </Scrollport>
      </section>
    </Show>
  );
}
