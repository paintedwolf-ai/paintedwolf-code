import { Show, createEffect, createMemo, createSignal, type Accessor } from "solid-js";
import { ActivitySummary, type ActivitySummaryState } from "../../briefing/ActivitySummary.tsx";
import { BriefingEvidenceLink } from "../../briefing/BriefingSummary.tsx";
import { retryBriefing, useBriefing, type BriefingTarget, type BriefingView } from "../../briefing/briefing-live.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../settings/editor/file-summary-settings.ts";
import type { ToolBriefingSlots } from "../../components/tool/tool-briefing-slots.ts";
import { useTranscriptDisclosure } from "../transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
import type { ToolPartView } from "./tool-part-model.ts";

/**
 * The host target for an activity card: its program activities and calls for
 * command cards, the call alone otherwise. The settled state is part of the
 * target, so a card that settles while open asks once for its new revision.
 */
export function activityBriefingTarget(part: ToolPartView, sessionId: string | undefined): BriefingTarget | null {
  if (!sessionId) return null;
  const activity = part.commandActivity;
  if (!activity) return { profileId: "tool_activity", ref: { session_id: sessionId, tool_call_ids: [part.toolCallId] } };
  const members: { program_id: string; activity_id: string }[] = [];
  let index = 0;
  for (const call of activity.calls) {
    const program = call.commandProgram;
    if (!program) continue;
    const member = activity.members[index++];
    if (member && member.execution_ids.length) members.push({ program_id: program.id, activity_id: member.id });
  }
  return {
    profileId: "tool_activity",
    ref: {
      session_id: sessionId,
      tool_call_ids: activity.calls.map((call) => call.toolCallId),
      ...(members.length ? { members } : {}),
      activity_revision: activity.fact.state,
    },
  };
}

/** Text a card has shown; kept while a newer revision of the same card is asked for. */
type HeldSummaryText = { purpose?: string; recordId?: string };

function sectionText(view: BriefingView, id: string): string | undefined {
  return view.snapshot?.result?.summary.find((section) => section.section_id === id)?.text || undefined;
}

function summaryPending(view: BriefingView): boolean {
  return view.snapshot?.status === "pending" && !view.stalled &&
    view.snapshot.pending_sections.some((id) => id === "purpose" || id === "change");
}

/** Maps the host's answer, and text held from an earlier revision, to what the region shows. */
function activitySummaryState(view: BriefingView, held: HeldSummaryText | null): ActivitySummaryState {
  const snapshot = view.snapshot;
  const purpose = sectionText(view, "purpose");
  const change = sectionText(view, "change");
  // A settled answer with no summary text counts as not arrived.
  const ended = view.stalled || snapshot?.status === "failed" || snapshot?.status === "complete";
  const recordId = snapshot?.result?.evidence_ref.record_id;
  if ((purpose || change) && recordId && !summaryPending(view)) {
    return { kind: "ready", purpose: purpose ?? held?.purpose, change, recordId };
  }
  const earlier = held?.purpose;
  return ended ? { kind: "missing", purpose: earlier } : { kind: "pending", purpose: earlier };
}

type Args = {
  part: Accessor<ToolPartView>;
  sessionId: Accessor<string | undefined>;
  projectId: Accessor<string | undefined>;
};

/**
 * Fills an activity card's summary and evidence places. It asks only once a
 * person opens the card; a Find reveal opens it without asking.
 */
export function useActivityBriefingSlots(args: Args): ToolBriefingSlots {
  const disclosure = useTranscriptDisclosure(() => transcriptDisclosureKey.tool(args.part().id));
  const target = createMemo(() => {
    if (!disclosure.userOpen() || !fileSummariesSettingKnown() || !fileSummariesEnabled()) return null;
    return activityBriefingTarget(args.part(), args.sessionId());
  }, null, { equals: (a, b) => JSON.stringify(a) === JSON.stringify(b) });
  const view = useBriefing(args.projectId, target);
  const [held, setHeld] = createSignal<HeldSummaryText | null>(null);
  createEffect(() => {
    const purpose = sectionText(view(), "purpose");
    if (purpose && !summaryPending(view())) setHeld({ purpose, recordId: view().snapshot?.result?.evidence_ref.record_id });
  });
  const state = createMemo(() => activitySummaryState(view(), held()));
  const running = () => {
    const activity = args.part().commandActivity;
    return activity ? activity.fact.state === "running" : args.part().status === "running";
  };
  const retry = () => {
    const projectId = args.projectId();
    const wanted = target();
    if (projectId && wanted) retryBriefing(projectId, wanted);
  };
  const reference = () => (state().kind === "ready" ? view().snapshot?.result?.evidence_ref : undefined);
  const summaryElement = <ActivitySummary state={state()} running={running()} onRetry={retry} />;
  const evidenceElement = <Show when={reference()} keyed>{(ref) => <BriefingEvidenceLink reference={ref} />}</Show>;
  return {
    get summary() {
      return target() ? summaryElement : undefined;
    },
    get evidence() {
      return reference() ? evidenceElement : undefined;
    },
  };
}
