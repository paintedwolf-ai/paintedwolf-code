import { createSignal, onCleanup } from "solid-js";
import { noticeFromUnknown, type AppNotice } from "../../notices/notice-model.ts";
import type { NoticeScope } from "../../notices/notice-scope.ts";
import { shouldReportToNoticeRail } from "../../notices/report-policy.ts";

type Submission = { pending: boolean; accepted?: boolean; notice?: AppNotice };

/** Keeps accepted decisions disabled until the host retires their cards. */
export function createCheckpointSubmission() {
  const [states, setStates] = createSignal<Record<string, Submission>>({});
  let retained: ReadonlySet<string> | undefined;
  let disposed = false;
  onCleanup(() => { disposed = true; });

  const finish = (id: string, notice?: AppNotice, accepted = false) => {
    if (disposed) return;
    setStates((previous) => {
      const next = { ...previous };
      if (retained && !retained.has(id)) delete next[id];
      else next[id] = { pending: false, accepted, notice };
      return next;
    });
  };

  return {
    pending: (id: string) => Boolean(states()[id]?.pending || states()[id]?.accepted),
    notice: (id: string) => states()[id]?.notice,
    retain(ids: readonly string[]) {
      retained = new Set(ids);
      setStates((previous) => Object.fromEntries(
        Object.entries(previous).filter(([id, state]) => retained?.has(id) || state.pending),
      ));
    },
    async run(id: string, scope: NoticeScope, operation: () => Promise<void>): Promise<boolean> {
      if (disposed || states()[id]?.pending || states()[id]?.accepted) return false;
      setStates((previous) => ({ ...previous, [id]: { pending: true } }));
      try {
        await operation();
        finish(id, undefined, true);
        return true;
      } catch (error) {
        finish(id, shouldReportToNoticeRail(error) ? noticeFromUnknown(error, scope) : undefined);
        return false;
      }
    },
  };
}
