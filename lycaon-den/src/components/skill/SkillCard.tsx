import { Show } from "solid-js";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import {
  SKILL_CHICKLET_NAME,
  skillActivationFromPart,
  skillOriginLabel,
  skillReadResource,
  skillResourceListing,
} from "../../chat/skill/skill-card-model.ts";
import { ToolContentLink } from "../tool/ToolContentLink.tsx";
import { StructuredToolBody } from "../tool/StructuredToolBody.tsx";
import { ToolPartShell } from "../tool/ToolPartShell.tsx";
import { TurnLoadFootLine } from "../tool/TurnLoadFootLine.tsx";
import { ToolResolutionFacts } from "../tool/ToolResolutionFacts.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
};

/** Discovery, pending, and rejected reads use the generic tool body. */
export function SkillCard(props: Props) {
  const skill = () => skillActivationFromPart(props.part);
  const resource = () => skillReadResource(props.part);
  return (
    <ToolPartShell
      part={props.part}
      layout={props.layout}
      projectId={props.projectId}
      sessionId={props.sessionId}
      name={SKILL_CHICKLET_NAME}
      testId="skill-part-card"
    >
      <Show
        when={skill()}
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
        {(activated) => (
          <div class="den-tool-part-card-structured" data-testid="skill-card-body">
            <ToolResolutionFacts part={props.part} />
            <div class="den-tool-part-card-section">
              <dl class="den-tool-part-card-facts">
                <div>
                  <dt>Skill</dt>
                  <dd data-testid="skill-card-name">{activated.name}</dd>
                </div>
                <Show when={resource()}>
                  {(path) => (
                    <div class="den-tool-part-card-facts--wide">
                      <dt>Resource</dt>
                      <dd data-testid="skill-card-resource"><code>{path()}</code></dd>
                    </div>
                  )}
                </Show>
                <Show when={!resource()}>
                  <div>
                    <dt>Source</dt>
                    <dd>{skillOriginLabel(activated)}</dd>
                  </div>
                  <Show when={activated.description?.trim()}>
                    {(description) => (
                      <div class="den-tool-part-card-facts--wide">
                        <dt>Description</dt>
                        <dd data-testid="skill-card-description">
                          {description()}
                        </dd>
                      </div>
                    )}
                  </Show>
                  <Show when={activated.dir?.trim()}>
                    {(dir) => (
                      <div class="den-tool-part-card-facts--wide">
                        <dt>Directory</dt>
                        {/* Provenance label, not an openable path: a bundled skill's
                            dir is config-relative and resolves to nothing on disk. */}
                        <dd data-testid="skill-card-dir">
                          <code>{dir()}</code>
                        </dd>
                      </div>
                    )}
                  </Show>
                  <Show when={activated.compatibility?.trim()}>
                    {(compatibility) => (
                      <div class="den-tool-part-card-facts--wide">
                        <dt>Compatibility</dt>
                        <dd>{compatibility()}</dd>
                      </div>
                    )}
                  </Show>
                  <Show when={activated.license?.trim()}>
                    {(license) => (
                      <div>
                        <dt>License</dt>
                        <dd>{license()}</dd>
                      </div>
                    )}
                  </Show>
                  <Show when={activated.allowed_tools?.trim()}>
                    {(allowed) => (
                      <div class="den-tool-part-card-facts--wide">
                        <dt>Declared tools</dt>
                        {/* The author's frontmatter. Said plainly because this host
                            reads it for display and never enforces it. */}
                        <dd data-testid="skill-card-allowed-tools">
                          {allowed()} — not enforced
                        </dd>
                      </div>
                    )}
                  </Show>
                </Show>
              </dl>
            </div>
            <Show when={!resource() && skillResourceListing(activated)} keyed>
              {(listing) => (
                <div class="den-tool-part-card-section">
                  <p class="den-tool-part-card-section-label">Files</p>
                  <Show when={listing.files.length > 0}>
                    <ToolContentLink pane="skill-files" label="Skill files" projectId={props.projectId}
                      content={{ kind: "inline", text: listing.files.join("\n") }} />
                  </Show>
                  <Show when={listing.omitted > 0}>
                    <p class="den-tool-part-card-note">
                      +{listing.omitted} more not listed
                    </p>
                  </Show>
                </div>
              )}
            </Show>
            <Show when={activated.instructions.trim()} keyed>
              {(instructions) => (
                <div class="den-tool-part-card-section">
                  <p class="den-tool-part-card-section-label">{resource() ? "Resource content" : "Instructions"}</p>
                  <ToolContentLink pane="skill-instructions" label={resource() ? "Resource content" : "Instructions"} projectId={props.projectId}
                    content={{ kind: "inline", text: instructions }} />
                </div>
              )}
            </Show>
          </div>
        )}
      </Show>
      <TurnLoadFootLine part={props.part} />
    </ToolPartShell>
  );
}
