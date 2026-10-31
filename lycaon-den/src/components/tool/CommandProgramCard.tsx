import { For, Index, Show, createMemo, createResource, createSignal, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ToolFact } from "../../chat/tool/tool-presentation-contract.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { buildCommandProgramPresentation } from "../../chat/tool/command-program-model.ts";
import { StructuredToolBody } from "./StructuredToolBody.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";
import { RetainedToolContent } from "./RetainedToolContent.tsx";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { TurnLoadFootLine } from "./TurnLoadFootLine.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
};

const STAGE_PAGE = 20;

function Facts(props: { facts: ToolFact[] }) {
  return (
    <dl class="den-tool-part-card-facts">
      <Index each={props.facts}>
        {(fact) => (
          <div classList={{ "den-tool-part-card-facts--wide": fact().wide }}>
            <dt>{fact().label}</dt>
            <dd>{fact().value}</dd>
          </div>
        )}
      </Index>
    </dl>
  );
}

function Section(props: { label: string; testId: string; children: JSX.Element }) {
  return (
    <section class="den-tool-part-card-section" data-testid={props.testId} aria-label={props.label}>
      <h4 class="den-tool-part-card-section-label">{props.label}</h4>
      {props.children}
    </section>
  );
}

/** One card per command call: plain sections inside the existing disclosure. */
export function CommandProgramCard(props: Props) {
  return (
    <ToolPartShell part={props.part} layout={props.layout} sessionId={props.sessionId} projectId={props.projectId} client={props.client}>
      <Show when={props.part.commandProgram} keyed>
        {(program) => <CommandProgramBody {...props} program={program} />}
      </Show>
      <StructuredToolBody part={props.part} sessionId={props.sessionId} projectId={props.projectId} rootRefs={props.rootRefs} omitArgs />
      <TurnLoadFootLine part={props.part} />
    </ToolPartShell>
  );
}

function CommandProgramBody(props: Props & { program: CommandProgramReceipt }) {
  // Host order and identity: each page is one cursor; paging is not disclosure.
  const [cursors, setCursors] = createSignal<string[]>([]);
  const cursor = () => cursors().at(-1);
  const [page] = createResource(
    () => {
      const sessionId = props.sessionId?.trim();
      const client = props.client ?? getLycaonClient();
      if (!sessionId || !client) return undefined;
      // A settled or changed program state refetches the same cursor.
      return { client, sessionId, programId: props.program.id, cursor: cursor(), state: props.program.state, settled: props.program.settled_at };
    },
    ({ client, sessionId, programId, cursor }) => client.getCommandProgramReceipt(sessionId, programId, { limit: STAGE_PAGE, cursor }),
  );
  // An errored resource throws on read; the card then shows only host summary facts.
  const loaded = () => (page.state === "ready" || page.state === "refreshing" ? page.latest : undefined);
  const view = createMemo(() => buildCommandProgramPresentation(loaded()?.program ?? props.program, loaded(), props.part.feedback));
  const source = createMemo(() => {
    const text = props.part.args?.["command"];
    return typeof text === "string" ? text : undefined;
  });
  return (
    <div class="den-tool-part-card-structured" data-testid="command-program-card" data-program-state={props.program.state}>
      <p class="den-tool-part-card-note" data-testid="command-program-summary">{view().summary.join(" · ")}</p>
      <Section label="Program" testId="command-program-section-program">
        <Show when={view().program.length > 0}>
          <ol class="den-command-program-statements">
            <For each={view().program}>{(statement) => <li><code>{statement}</code></li>}</For>
          </ol>
        </Show>
        <Show when={source()} keyed fallback={<Show when={props.part.argsReference}><RetainedToolContent field="args" /></Show>}>
          {(text) => <ToolContentLink pane="args" label="Full program" content={{ kind: "inline", text }} projectId={props.projectId} />}
        </Show>
      </Section>
      <Section label="Result" testId="command-program-section-result">
        <Facts facts={view().result} />
      </Section>
      <Show when={view().stages.length > 0 || cursors().length > 0}>
        <Section label="Stages" testId="command-program-section-stages">
          <ol class="den-command-program-stages">
            <For each={view().stages}>
              {(stage) => (
                <li class="den-command-program-stage" data-testid="command-program-stage" data-execution-id={stage.id}>
                  <p class="den-command-program-stage-head">
                    <code>{stage.command}</code>
                    <span>{stage.state}</span>
                    <span>{stage.provenance}</span>
                    <Show when={stage.source}>{(where) => <span>{where()}</span>}</Show>
                  </p>
                  <Facts facts={stage.facts} />
                  <For each={stage.outputs}>
                    {(output) => (
                      <div class="den-tool-part-card-section" data-testid="command-program-stage-output" data-stream={output.stream}>
                        <pre class="den-command-program-output-preview">{output.preview}</pre>
                        <p class="den-command-program-stage-head">
                          <ToolContentLink pane="output" label={`${output.label} preview`} content={{ kind: "inline", text: output.preview }} projectId={props.projectId} />
                          <Show when={output.truncated}><span>Preview of retained output</span></Show>
                          <Show when={!output.complete}><span>Stream ended early</span></Show>
                        </p>
                      </div>
                    )}
                  </For>
                </li>
              )}
            </For>
          </ol>
          <Show when={loaded()?.executions_omitted}>
            {(omitted) => <p class="den-tool-part-card-note">{omitted()} more stages are in retained receipts</p>}
          </Show>
          <Show when={cursors().length > 0 || loaded()?.next_cursor}>
          <div class="den-command-program-paging" role="group" aria-label="Stage pages">
            <button type="button" class="den-chat-content-link" data-testid="command-program-previous" disabled={cursors().length === 0} onClick={() => setCursors((all) => all.slice(0, -1))}>
              Previous stages
            </button>
            <button type="button" class="den-chat-content-link" data-testid="command-program-next" disabled={!loaded()?.next_cursor} onClick={() => {
              const next = loaded()?.next_cursor;
              if (next) setCursors((all) => [...all, next]);
            }}>
              Next stages
            </button>
          </div>
          </Show>
        </Section>
      </Show>
      <Show when={view().changes.length > 0}>
        <Section label="Changes" testId="command-program-section-changes">
          <Facts facts={view().changes} />
        </Section>
      </Show>
      <Show when={view().remaining.length > 0}>
        <Section label="Remaining work" testId="command-program-section-remaining">
          <Facts facts={view().remaining} />
        </Section>
      </Show>
      <Show when={page.state === "errored"}>
        <p class="den-tool-part-card-note" data-testid="command-program-receipt-unavailable">Program receipts are unavailable right now.</p>
      </Show>
    </div>
  );
}
