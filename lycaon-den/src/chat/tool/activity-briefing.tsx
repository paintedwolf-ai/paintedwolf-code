import { Show, createMemo, type Accessor } from "solid-js";
import { BriefingEvidenceLink, BriefingSummary, snapshotSections } from "../../briefing/BriefingSummary.tsx";
import { useBriefing, type BriefingTarget } from "../../briefing/briefing-live.ts";
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

type Args = {
  part: Accessor<ToolPartView>;
  sessionId: Accessor<string | undefined>;
  projectId: Accessor<string | undefined>;
};

/**
 * Fills an activity card's purpose, result and evidence places. It asks only
 * once a person opens the card; a Find reveal opens it without asking.
 */
export function useActivityBriefingSlots(args: Args): ToolBriefingSlots {
  const disclosure = useTranscriptDisclosure(() => transcriptDisclosureKey.tool(args.part().id));
  const target = createMemo(() => {
    if (!disclosure.userOpen() || !fileSummariesSettingKnown() || !fileSummariesEnabled()) return null;
    return activityBriefingTarget(args.part(), args.sessionId());
  }, null, { equals: (a, b) => JSON.stringify(a) === JSON.stringify(b) });
  const view = useBriefing(args.projectId, target);
  const sections = createMemo(() => snapshotSections(view().snapshot, view().stalled));
  const purpose = createMemo(() => sections().filter((section) => section.id === "purpose"));
  const change = createMemo(() => sections().filter((section) => section.id === "change"));
  const recordId = () => view().snapshot?.result?.evidence_ref.record_id ?? null;
  const reference = () => view().snapshot?.result?.evidence_ref;
  const purposeElement = <BriefingSummary sections={purpose()} recordId={recordId()} data-testid="activity-briefing-purpose" />;
  const changeElement = <BriefingSummary sections={change()} recordId={recordId()} data-testid="activity-briefing-change" />;
  const evidenceElement = <Show when={reference()}>{(ref) => <BriefingEvidenceLink reference={ref()} />}</Show>;
  return {
    get purpose() {
      return purpose().length ? purposeElement : undefined;
    },
    get result() {
      return change().length ? changeElement : undefined;
    },
    get evidence() {
      return reference() ? evidenceElement : undefined;
    },
  };
}
