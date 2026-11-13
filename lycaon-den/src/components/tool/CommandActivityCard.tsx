import { Show, createMemo, createResource, splitProps } from "solid-js";
import type { ToolBriefingSlots } from "./tool-briefing-slots.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { activityExceptions, activityOutput, activityResultLine } from "../../chat/tool/command-activity-details.ts";
import type { CommandReceiptContent } from "../../chat/transcript/content/chat-content-document.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { ActivityCalls, ActivityCommand, ActivityExceptions, ActivityFooter, ActivityOutputRegion, ActivityResult, ActivitySteps } from "./CommandActivityRegions.tsx";
import { TurnLoadFootLine } from "./TurnLoadFootLine.tsx";
import { ToolPartShell } from "./ToolPartShell.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
  slots?: ToolBriefingSlots;
};

/** Receipt scopes previewed per card; the Receipt link covers every scope. */
const LOADED_SCOPES = 3;
const EXECUTIONS_PER_PROGRAM = 6;

export function CommandActivityCard(props: Props) {
  // The activity layout places the briefing itself, so the shell never sees it.
  const [, shell] = splitProps(props, ["slots"]);
  return <ToolPartShell {...shell}><Show when={props.part.commandActivity}>{activity => <CommandActivityDetails {...props} activity={activity} />}</Show><TurnLoadFootLine part={props.part} /></ToolPartShell>;
}

function CommandActivityDetails(props: Props & { activity: () => NonNullable<ToolPartView["commandActivity"]> }) {
  const activity = props.activity;
  const snapshot = createMemo(() => {
    const client = props.client ?? getLycaonClient();
    if (!client || !props.sessionId) return undefined;
    const requests = activity().receiptScopes.slice(0, LOADED_SCOPES);
    const programs = requests.map(scope => activity().calls.find(call => call.commandProgram?.id === scope.programId)?.commandProgram);
    const key = JSON.stringify([requests, programs.map(program => [program?.state, program?.settled_at]), activity().fact.state, activity().fact.execution_count, activity().fact.failed_count]);
    return { client, sessionId: props.sessionId, requests, key };
  }, undefined, { equals: (a, b) => a?.client === b?.client && a?.sessionId === b?.sessionId && a?.key === b?.key });
  const [pages] = createResource(snapshot, ({ client, sessionId, requests }) =>
    Promise.all(requests.map(request => client.getCommandProgramReceipt(sessionId, request.programId, { limit: EXECUTIONS_PER_PROGRAM, cursor: undefined, activity_id: request.activityId }))),
  );
  const loaded = () => pages.state === "ready" || pages.state === "refreshing" ? pages.latest ?? [] : [];
  const executions = createMemo(() => loaded().flatMap(page => page?.executions ?? []));
  const feedback = createMemo(() => activity().fact.execution_ids.length === 0
    ? props.part.feedback
    : props.part.feedback?.filter(entry => executions().some(row => row.stop_code === entry.code || row.host_stop_code === entry.code)));
  const exceptions = createMemo(() => activityExceptions(activity(), executions(), feedback()));
  const receipts = createMemo<CommandReceiptContent>(() => ({
    kind: "command_receipts",
    programs: activity().receiptScopes.map(scope => ({ id: scope.programId, activityId: scope.activityId })),
    context: JSON.stringify({ activities: activity().members, facts: exceptions(), feedback: props.part.feedback }),
  }));
  return <div class="den-activity-detail" data-testid="command-activity-details">
    <ActivityCommand activity={activity()} projectId={props.projectId} />
    <Show when={props.slots?.purpose}>{(purpose) => <div class="den-activity-purpose" data-testid="activity-purpose">{purpose()}</div>}</Show>
    <ActivityResult line={activityResultLine(activity(), executions())}>{props.slots?.result}</ActivityResult>
    <ActivityOutputRegion output={activityOutput(activity(), executions())} />
    <ActivitySteps activity={activity()} projectId={props.projectId} />
    <ActivityCalls activity={activity()} projectId={props.projectId} />
    <ActivityExceptions facts={exceptions()} />
    <Show when={pages.state === "errored"}><p class="den-activity-note">Execution details are unavailable. The recorded activity is still shown.</p></Show>
    <ActivityFooter activity={activity()} projectId={props.projectId} receipts={receipts()} evidence={props.slots?.evidence} />
  </div>;
}
