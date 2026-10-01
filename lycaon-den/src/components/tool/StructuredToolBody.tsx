import { Index, Match, Show, Switch, createMemo, createSignal } from "solid-js";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { buildStructuredToolPresentation } from "../../chat/tool/tool-part-structured.ts";
import type {
  StructuredToolSection,
  ToolFact,
} from "../../chat/tool/tool-presentation-contract.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { HostSecretRedactionMeta } from "../../api/types.ts";
import { BackgroundProcessPanel } from "./BackgroundProcessPanel.tsx";
import { RetainedToolContent } from "./RetainedToolContent.tsx";
import type { ReadLogDigestView } from "../../chat/tool/read-tool-output.ts";
import { ToolContentLink } from "./ToolContentLink.tsx";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { ToolResolutionFacts } from "./ToolResolutionFacts.tsx";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { LogDigestOutline } from "../LogDigestOutline.tsx";
import { NetworkChicklet } from "./NetworkChicklet.tsx";
import { RedactionChicklet } from "./RedactionChicklet.tsx";
import { RedactedText } from "../transcript/RedactionMark.tsx";
import {
  spansForField,
  type ToolArgsRedaction,
} from "../../chat/transcript/content/redaction-spans.ts";
import { openInSearch } from "../../search/search-nav.ts";
import { exploreRelatedToolCallsQuery } from "../../search/search-query-model.ts";
import { SourcePathLink } from "../source/SourcePathLink.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ContextMenu, type ContextMenuAnchor } from "../ContextMenu.tsx";
import { spillPathMenuItems } from "../spill-path-menu-items.ts";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { chatRefForToolRaw } from "../../chat/composer/search-add-to-chat.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";
import { useChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";
import {
  transcriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "../../chat/transcript/presentation/transcript-disclosure-key.ts";

type Props = {
  part: ToolPartView;
  sessionId?: string;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
};

function FactsGrid(props: {
  facts: ToolFact[];
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  sessionId?: string;
  toolCallId?: string;
  argsRedaction?: ToolArgsRedaction | null;
}) {
  const pid = () => props.projectId?.trim() || undefined;
  // Redaction spans are scoped to the rendered field.
  const factSpans = (fact: ToolFact) => {
    const meta = props.argsRedaction?.meta;
    if (!meta || !fact.redactionField) return [];
    return spansForField({ host_secret_redaction: meta }, fact.redactionField);
  };
  const factValue = (fact: ToolFact) => {
    const meta = props.argsRedaction?.meta;
    if (!meta || !fact.redactionField) return fact.value;
    return (
      <RedactedText
        message={{ host_secret_redaction: meta }}
        field={fact.redactionField}
        text={fact.value}
      />
    );
  };
  /** Redacted paths render as plain text. */
  const linkTarget = (fact: ToolFact) =>
    fact.path && pid() && factSpans(fact).length === 0 ? fact.path : undefined;
  return (
    <dl class="den-tool-part-card-facts">
      <Index each={props.facts}>
        {(fact) => (
          <div classList={{ "den-tool-part-card-facts--wide": fact().wide }}>
            <dt>{fact().label}</dt>
            <dd>
              <Show
                when={fact().hostDataSpill ? fact().value : undefined}
                keyed
                fallback={
                  <ShowLatest
                    when={linkTarget(fact())}
                    fallback={factValue(fact())}
                  >
                    {(fp) => (
                      <SourcePathLink
                        projectId={pid() ?? ""}
                        path={fp().path}
                        rootId={fp().rootId}
                        entryKind={fp().entryKind ?? "unknown"}
                        line={fp().line}
                        label={fact().value}
                        rootRefs={props.rootRefs}
                      />
                    )}
                  </ShowLatest>
                }
              >
                {(spillPath) => (
                  <SpillPathChrome
                    spillPath={spillPath}
                    projectId={props.projectId}
                    sessionId={props.sessionId}
                    toolCallId={props.toolCallId}
                  />
                )}
              </Show>
            </dd>
          </div>
        )}
      </Index>
    </dl>
  );
}

function SpillPathChrome(props: {
  spillPath: string;
  projectId?: string;
  sessionId?: string;
  toolCallId?: string;
}) {
  const [menu, setMenu] = createSignal<ContextMenuAnchor | null>(null);
  const chatDestination = useChatDestinationScope();
  const path = () => props.spillPath.trim();
  return (
    <>
      <span
        class="den-tool-part-card-compaction-spill"
        data-testid="tool-compaction-spill"
        data-path={path() || undefined}
        data-project-id={props.projectId?.trim() || undefined}
        data-session-id={props.sessionId?.trim() || undefined}
        data-tool-call-id={props.toolCallId?.trim() || undefined}
        onContextMenu={(e) => {
          if (!path()) return;
          e.preventDefault();
          e.stopPropagation();
          setMenu({ x: e.clientX, y: e.clientY });
        }}
      >
        <code>{path()}</code>
      </span>
      <Show when={menu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            items={spillPathMenuItems({
              spillPath: path(),
              projectId: props.projectId,
              sessionId: props.sessionId,
              toolCallId: props.toolCallId,
              chatDestination: chatDestination(),
            })}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
    </>
  );
}

function Section(props: {
  section: StructuredToolSection;
  index: number;
  sessionId?: string;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  networkDisclosureKey?: TranscriptDisclosureKey;
  toolCallId?: string;
  redaction?: HostSecretRedactionMeta | null;
}) {
  return (
    <Switch>
      <Match when={props.section.kind === "facts"}>
        <FactsGrid
          facts={(props.section as { facts: ToolFact[] }).facts}
          projectId={props.projectId}
          rootRefs={props.rootRefs}
          sessionId={props.sessionId}
          toolCallId={props.toolCallId}
        />
      </Match>
      <Match when={props.section.kind === "note"}>
        <p class="den-tool-part-card-note">
          {(props.section as { text: string }).text}
        </p>
      </Match>
      <Match when={props.section.kind === "compaction"}>
        <CompactionSection
          section={
            props.section as {
              kind: "compaction";
              banner: string;
              spillPath?: string;
            }
          }
          projectId={props.projectId}
          sessionId={props.sessionId}
          toolCallId={props.toolCallId}
        />
      </Match>
      <Match when={props.section.kind === "log_digest"}>
        <LogDigestOutline
          view={(props.section as { view: ReadLogDigestView }).view}
          projectId={props.projectId}
          rootRefs={props.rootRefs}
        />
      </Match>
      <Match when={props.section.kind === "network"}>
        <NetworkChicklet
          externalAccess={
            (
              props.section as {
                externalAccess: import("../../api/types.ts").ExternalAccess;
              }
            ).externalAccess
          }
          sessionId={props.sessionId}
          disclosureKey={props.networkDisclosureKey}
        />
      </Match>
      <Match when={props.section.kind === "background_process"}>
        <BackgroundProcessSection
          sessionId={props.sessionId}
          section={
            props.section as {
              kind: "background_process";
              handle: string;
              running: boolean;
            }
          }
        />
      </Match>
      <Match when={props.section.kind === "content"}>
        <ContentSection
          index={props.index}
          projectId={props.projectId}
          redaction={props.redaction}
          section={props.section as Extract<StructuredToolSection, { kind: "content" }>}
        />
      </Match>
    </Switch>
  );
}

function ContentSection(props: {
  index: number;
  section: Extract<StructuredToolSection, { kind: "content" }>;
  projectId?: string;
  redaction?: HostSecretRedactionMeta | null;
}) {
  return <ToolContentLink pane={`output:${props.index}`}
    label={props.section.label ?? "Output"} projectId={props.projectId}
    content={{ kind: "inline", text: props.section.text, redaction: props.redaction ?? undefined }} />;
}

function CompactionSection(props: {
  section: { banner: string; spillPath?: string };
  projectId?: string;
  sessionId?: string;
  toolCallId?: string;
}) {
  return (
    <div class="den-tool-part-card-compaction">
      <p class="den-tool-part-card-compaction-banner">{props.section.banner}</p>
      <Show when={props.section.spillPath} keyed>
        {(path) => (
          <p>
            Full output:{" "}
            <SpillPathChrome
              spillPath={path}
              projectId={props.projectId}
              sessionId={props.sessionId}
              toolCallId={props.toolCallId}
            />
          </p>
        )}
      </Show>
    </div>
  );
}

function BackgroundProcessSection(props: {
  sessionId?: string;
  section: { handle: string; running: boolean };
}) {
  return (
    <Show when={props.sessionId?.trim()} keyed>
      {(id) => (
        <BackgroundProcessPanel
          sessionId={id}
          processId={props.section.handle}
          initialRunning={props.section.running}
        />
      )}
    </Show>
  );
}

function RawOutputChrome(props: {
  part: ToolPartView;
  sessionId?: string;
  projectId?: string;
}) {
  const toolContent = useToolContent();
  const rawContent = createMemo<ChatContentDocument["content"]>(() => {
    const reference = props.part.outputReference;
    return reference
      ? { kind: "retained", reference }
      : { kind: "inline", text: props.part.output ?? "", redaction: props.part.redaction ?? undefined };
  });
  const chatDestination = useChatDestinationScope();
  const query = createMemo(() => {
    const sessionId = props.sessionId?.trim();
    const projectId = props.projectId?.trim();
    const toolCallId = props.part.toolCallId?.trim();
    if (!sessionId || !projectId || !toolCallId) return undefined;
    return exploreRelatedToolCallsQuery({ toolCallId, sessionId, tool: props.part.tool });
  });

  const searchHitRef = createMemo(() => {
    const projectId = props.projectId?.trim() ?? "";
    const sessionId = props.sessionId?.trim() ?? "";
    const toolCallId = props.part.toolCallId?.trim() ?? "";
    return chatRefForToolRaw({
      projectId,
      sessionId,
      toolCallId,
      name: props.part.tool?.trim() || toolCallId,
    });
  });

  const onAddToChat = () => {
    const ref = searchHitRef();
    if (ref) void addToChat(ref, { destination: chatDestination() });
  };

  return (
    <div
      class="den-tool-part-card-section"
      data-testid="tool-raw-output-chrome"
      data-tool-call-id={props.part.toolCallId?.trim() || undefined}
      data-session-id={props.sessionId?.trim() || undefined}
      data-project-id={props.projectId?.trim() || undefined}
      draggable={searchHitRef() ? true : undefined}
      onDragStart={(event) => startChatAttachmentDrag(event, searchHitRef())}
    >
      <div class="den-tool-part-card-structured">
        <Show when={searchHitRef()}>
          <div class="den-tool-part-card-section">
            <ToolContentLink
              pane="output"
              label="Raw output"
              projectId={props.projectId}
              revealOffset={toolContent?.outputOffset()}
              content={rawContent()}
            />
          </div>
        </Show>
        <Show when={query()}>
          {(q) => (
            <div class="den-tool-part-card-section">
              <button
                type="button"
                class="den-chat-content-link"
                data-testid="tool-related-search-link"
                onClick={() => {
                  const projectId = props.projectId?.trim();
                  if (!projectId) return;
                  openInSearch(projectId, q());
                }}
              >
                Find related tool calls
              </button>
            </div>
          )}
        </Show>
        <Show when={searchHitRef()}>
          <div class="den-tool-part-card-section">
            <button
              type="button"
              class="den-chat-content-link"
              data-testid="tool-raw-add-to-chat"
              onClick={onAddToChat}
            >
              Add to chat
            </button>
          </div>
        </Show>
      </div>
    </div>
  );
}

export function StructuredToolBody(props: Props) {
  const view = createMemo(() =>
    buildStructuredToolPresentation(props.part),
  );

  return (
    <div class="den-tool-part-card-structured">
      <Show when={props.part.redaction?.spans?.length}>
        <div class="den-tool-part-card-section">
          <RedactionChicklet message={{ host_secret_redaction: props.part.redaction ?? undefined }} />
        </div>
      </Show>
      <Show when={view().evidenceHandle} keyed>
        {(handle) => (
          <div class="den-tool-part-card-section">
            <FactsGrid
              facts={[{ label: "Evidence", value: handle }]}
              projectId={props.projectId}
              rootRefs={props.rootRefs}
              sessionId={props.sessionId}
              toolCallId={props.part.toolCallId}
            />
          </div>
        )}
      </Show>
      <Show when={view().argsFacts.length > 0}>
        <div class="den-tool-part-card-section">
          <p class="den-tool-part-card-section-label">Input</p>
          <FactsGrid
            facts={view().argsFacts}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            sessionId={props.sessionId}
            toolCallId={props.part.toolCallId}
            argsRedaction={props.part.argsRedaction}
          />
        </div>
      </Show>
      <Show when={props.part.argsReference}><RetainedToolContent field="args" /></Show>
      <Show when={props.part.tool === "request_tools" || props.part.tool === "skills_read"}>
        <ToolResolutionFacts part={props.part} />
      </Show>
      <Show when={props.part.outputReference && !view().rawOutputAvailable}><RetainedToolContent field="output" /></Show>
      <Index each={view().sections}>
        {(section, index) => {
          const redaction = createMemo(() => {
            const value = section();
            return value.kind === "content" && value.text === props.part.output ? props.part.redaction : undefined;
          });
          return (
          <div class="den-tool-part-card-section">
            <Section
              index={index}
              section={section()}
              redaction={redaction()}
              sessionId={props.sessionId}
              projectId={props.projectId}
              rootRefs={props.rootRefs}
              toolCallId={props.part.toolCallId}
              networkDisclosureKey={
                section().kind === "network" && props.part.id
                  ? transcriptDisclosureKey.toolNetwork(props.part.id)
                  : undefined
              }
            />
          </div>
          );
        }}
      </Index>
      <Show when={view().rawOutputAvailable}>
        <RawOutputChrome
          part={props.part}
          sessionId={props.sessionId}
          projectId={props.projectId}
        />
      </Show>
      <Show
        when={
          view().sections.length === 0 &&
          view().argsFacts.length === 0 &&
          !view().evidenceHandle &&
          !view().rawOutputAvailable
        }
      >
        <p class="den-tool-part-card-note">—</p>
      </Show>
    </div>
  );
}
