import { Show, createEffect, createMemo } from "solid-js";
import { toolDetailTarget } from "../../chat/tool/tool-detail-target.ts";
import { buildStructuredToolPresentation } from "../../chat/tool/tool-part-structured.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { visualFromPart } from "../../chat/visual/visual-artifact-model.ts";
import { visualProducerEntryKey } from "../../chat/visual/visual-artifact-canonical.ts";
import { TranscriptVisualSlot } from "../transcript/TranscriptVisualSlot.tsx";
import { ActivityTarget } from "./ActivityTarget.tsx";
import { StructuredToolBody } from "./StructuredToolBody.tsx";
import { ToolPartShell } from "./ToolPartShell.tsx";
import type { ToolBriefingSlots } from "./tool-briefing-slots.ts";
import { TurnLoadFootLine } from "./TurnLoadFootLine.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
  slots?: ToolBriefingSlots;
};

/** Collapsible card for native tool results (read / write / command / other). */
export function GenericToolCard(props: Props) {
  const visual = () => visualFromPart(props.part);
  const detail = createMemo(() => toolDetailTarget(props.part));
  createEffect(() => { buildStructuredToolPresentation(props.part); });
  return (
    <ToolPartShell slots={props.slots} part={props.part} layout={props.layout} sessionId={props.sessionId} projectId={props.projectId} client={props.client}
      target={<Show when={detail()} keyed>{(shown) => <ActivityTarget target={shown.target} noun={shown.noun} />}</Show>}>
      <StructuredToolBody
        part={props.part}
        sessionId={props.sessionId}
        projectId={props.projectId}
        rootRefs={props.rootRefs}
        omitArguments={detail()?.omitArguments}
      />
      {/* Captured evidence shares the tool disclosure. */}
      <Show when={visual()?.id} keyed>
        {(id) => {
          const art = visual();
          if (!art || art.id !== id) return null;
          return (
            <TranscriptVisualSlot
              artifact={art}
              sessionId={props.sessionId}
              projectId={props.projectId}
              client={props.client}
              entryKey={visualProducerEntryKey(id)}
              toolOutput={props.part.output}
            />
          );
        }}
      </Show>
      <TurnLoadFootLine part={props.part} />
    </ToolPartShell>
  );
}
