import { Show } from "solid-js";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import {
  VERDICT_CHICKLET_NAME,
  verdictOutcomeFromPart,
  verdictStatusLine,
} from "../../chat/verdict/verdict-card-model.ts";
import { CitationEvidenceSection } from "../citation/CitationEvidenceSection.tsx";
import { StructuredToolBody } from "../tool/StructuredToolBody.tsx";
import { ToolPartShell } from "../tool/ToolPartShell.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
};

/**
 * Recorded review_loop verdict card. The chicklet is the shared tool shell with
 * a body built from the host's verdict projection: the ruling, its lifecycle
 * line, and the validated citation grounding.
 */
export function VerdictCard(props: Props) {
  const outcome = () => verdictOutcomeFromPart(props.part);
  return (
    <ToolPartShell
      part={props.part}
      layout={props.layout}
      projectId={props.projectId}
      sessionId={props.sessionId}
      name={VERDICT_CHICKLET_NAME}
      testId="verdict-part-card"
    >
      <Show
        when={outcome()}
        keyed
        fallback={
          <StructuredToolBody
            part={props.part}
            sessionId={props.sessionId}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
          />
        }
      >
        {(recorded) => (
          <div class="den-tool-part-card-structured" data-testid="verdict-card-body">
            <div class="den-tool-part-card-section">
              <dl class="den-tool-part-card-facts">
                <div>
                  <dt>Verdict</dt>
                  <dd data-testid="verdict-card-value">{recorded.verdict}</dd>
                </div>
                <Show when={recorded.phase?.trim()}>
                  {(phase) => (
                    <div>
                      <dt>Phase</dt>
                      <dd>{phase()}</dd>
                    </div>
                  )}
                </Show>
                <div class="den-tool-part-card-facts--wide">
                  <dt>Outcome</dt>
                  <dd data-testid="verdict-card-status">{verdictStatusLine(recorded)}</dd>
                </div>
              </dl>
            </div>
            <Show when={recorded.grounding} keyed>
              {(grounding) => (
                <CitationEvidenceSection
                  grounding={grounding}
                  sectionClass="den-tool-part-card-section"
                  testId="verdict-card-grounding"
                  projectId={props.projectId}
                  rootRefs={props.rootRefs}
                />
              )}
            </Show>
          </div>
        )}
      </Show>
    </ToolPartShell>
  );
}
