import { Show, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type { AppNotice } from "../notices/notice-model.ts";
import type { NoticeStore } from "../notices/notice-store.ts";
import { selectSessionNotices } from "../notices/notice-select.ts";
import {
  SPEND_CEILING_NOTICE_CODE,
} from "../settings/budgets/spend-ceiling-readout.ts";
import { raiseEffectiveSpendCeiling, type SpendCeilingUpdate } from "../settings/budgets/spend-ceiling-actions.ts";
import { SystemNudge } from "./SystemNudge.tsx";

type Props = {
  client: LycaonClient;
  notices: NoticeStore;
  sessionId: string;
  projectId: string;
  /** Latest session estimate already received by the cost store. */
  spentUsd?: number;
  /** Leave unsent composer content ready for an explicit send. */
  hasUnsentContent?: boolean;
  /** Continue the stopped work after the ceiling is raised. */
  onResume: () => boolean | void | Promise<boolean | void>;
  onLimitsUpdated?: (update: SpendCeilingUpdate) => void;
  /** Navigate to Settings → Advanced → Budgets. */
  onOpenBudgets: () => void;
};

/** Returns the ceiling notice for one session. */
export function spendCeilingReachedNotice(
  notices: NoticeStore,
  sessionId: string,
): AppNotice | undefined {
  return selectSessionNotices(notices.index(), sessionId).find(
    (n) => n.code?.trim() === SPEND_CEILING_NOTICE_CODE,
  );
}

/** Shows actions for a parked session. */
export function SpendCeilingReachedNudge(props: Props) {
  const [busy, setBusy] = createSignal(false);
  const [raisedNoticeId, setRaisedNoticeId] = createSignal<string>();
  const [failure, setFailure] = createSignal<{ noticeId: string; message: string }>();

  const notice = createMemo(() =>
    spendCeilingReachedNotice(props.notices, props.sessionId),
  );

  const raiseAndResume = async () => {
    const row = notice();
    if (!row || busy()) return;
    const resume = !props.hasUnsentContent;
    setBusy(true);
    setFailure(undefined);
    try {
      if (raisedNoticeId() !== row.id) {
        const summary = await props.client.getCostSummary(props.sessionId);
        const cached = Number.isFinite(props.spentUsd) ? props.spentUsd ?? 0 : 0;
        const spent = Math.max(cached, (summary?.estimated_nano_usd ?? 0) / 1e9);
        const update = await raiseEffectiveSpendCeiling(props.client, props.projectId, spent);
        props.onLimitsUpdated?.(update);
        setRaisedNoticeId(row.id);
      }
      if (resume && !props.hasUnsentContent && await props.onResume() === false) throw new Error("Continuation was not submitted.");
      props.notices.dismiss(row.id);
    } catch {
      setFailure({
        noticeId: row.id,
        message: raisedNoticeId() === row.id
          ? "The ceiling was raised, but work could not resume. Try again."
          : "Could not raise the ceiling. Try again or open budgets.",
      });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Show when={notice()} keyed>
      {(row) => (
        <SystemNudge
          testId="spend-ceiling-reached-nudge"
          title={row.title}
          description={<>{row.message}<Show when={failure()?.noticeId === row.id}><div role="alert">{failure()?.message}</div></Show></>}
          primaryAction={{
            label: props.hasUnsentContent
              ? busy() ? "Raising…" : "Raise ceiling"
              : raisedNoticeId() === row.id
              ? busy() ? "Resuming…" : "Resume"
              : busy() ? "Raising…" : "Raise ceiling & resume",
            onClick: () => void raiseAndResume(),
            disabled: busy(),
          }}
          secondaryAction={{
            label: "Open budgets",
            onClick: () => props.onOpenBudgets(),
            disabled: busy(),
          }}
          onDismiss={() => props.notices.dismiss(row.id)}
        />
      )}
    </Show>
  );
}
