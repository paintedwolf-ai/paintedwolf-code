import { For, Show } from "solid-js";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import {
  LOCAL_AI_LABEL,
  formatProbability,
  turnLoadBoundaryCopy,
  turnLoadEngineName,
  turnLoadRowSummary,
  turnLoadToolDetail,
  type TurnLoadRow,
} from "../../chat/turnload/turn-load-rows.ts";
import { ToolPartShell } from "./ToolPartShell.tsx";

/** Decision rows share the completed-tool presentation. */
export function turnLoadToolPart(row: TurnLoadRow): ToolPartView {
  const summary = turnLoadRowSummary(row);
  return {
    id: row.key,
    toolCallId: "",
    assistantMessageId: row.assistantMessageId,
    workflowRunId: row.workflowRunId,
    messageId: row.anchorMessageId,
    tool: turnLoadEngineName(row.load),
    kind: "generic",
    status: "completed",
    title: summary.title,
    args: {},
    output: null,
    error: null,
    completion: { operation: summary.operation, state: summary.state },
  };
}

function ToolsBody(props: { row: TurnLoadRow }) {
  const load = () => props.row.load;
  const guides = () => load().guides;
  const addedTools = () => load().tools.filter((tool) => !tool.carried);
  const keptTools = () => load().tools.filter((tool) => tool.carried);
  return (
    <div class="den-tool-part-card-structured" data-testid="turn-load-body">
      <div class="den-tool-part-card-section">
        <dl class="den-tool-part-card-facts">
          <Show when={load().kind} keyed>
            {(kind) => (
              <div>
                <dt>Kind</dt>
                <dd data-testid="turn-load-kind">{kind.value} · {formatProbability(kind.confidence)}</dd>
              </div>
            )}
          </Show>
          <div class="den-tool-part-card-facts--wide">
            <dt>Newly offered</dt>
            <dd data-testid="turn-load-tools">
              <Show when={addedTools().length > 0} fallback="None">
                <For each={addedTools()}>
                  {(tool, index) => (
                    <>
                      <Show when={index() > 0}>, </Show>
                      <code>{tool.tool}</code> {turnLoadToolDetail(tool)}
                    </>
                  )}
                </For>
              </Show>
            </dd>
          </div>
          <Show when={keptTools().length > 0}>
            <div class="den-tool-part-card-facts--wide">
              <dt>Kept from earlier turns</dt>
              <dd data-testid="turn-load-kept-tools">
                <For each={keptTools()}>
                  {(tool, index) => (
                    <>
                      <Show when={index() > 0}>, </Show>
                      <code>{tool.tool}</code>
                    </>
                  )}
                </For>
              </dd>
            </div>
          </Show>
          <Show when={load().boundary} keyed>
            {(boundary) => (
              <div class="den-tool-part-card-facts--wide">
                <dt>Prompt cache</dt>
                <dd data-testid="turn-load-boundary" data-cache={boundary.cache}>
                  {turnLoadBoundaryCopy(boundary)}
                </dd>
              </div>
            )}
          </Show>
          <Show when={load().floor.length > 0}>
            <div class="den-tool-part-card-facts--wide">
              <dt>Floor</dt>
              <dd data-testid="turn-load-floor">
                {load().floor.length} always offered: {load().floor.join(", ")}
              </dd>
            </div>
          </Show>
          <Show when={guides()} keyed>
            {(counts) => (
              <div class="den-tool-part-card-facts--wide">
                <dt>Guides</dt>
                <dd data-testid="turn-load-guides">
                  {counts.rendered} rendered · {counts.omitted} left out of the prompt
                </dd>
              </div>
            )}
          </Show>
          <Show when={load().preloaded_skill} keyed>
            {(skill) => (
              <div class="den-tool-part-card-facts--wide">
                <dt>Preloaded skill</dt>
                <dd data-testid="turn-load-preloaded-skill"><code>{skill.name}</code> · {skill.score.toFixed(2)}</dd>
              </div>
            )}
          </Show>
          <EngineFact row={props.row} />
        </dl>
      </div>
    </div>
  );
}

function EngineFact(props: { row: TurnLoadRow }) {
  const load = () => props.row.load;
  return (
    <Show when={load().engine} keyed>
      {(engine) => (
        <div class="den-tool-part-card-facts--wide">
          <dt>Engine</dt>
          <dd data-testid="turn-load-engine">
            {engine.name} · {load().elapsed_ms} ms
          </dd>
        </div>
      )}
    </Show>
  );
}

export function TurnLoadCard(props: { row: TurnLoadRow; layout: TranscriptLayout; sessionId?: string }) {
  const part = () => turnLoadToolPart(props.row);
  return (
    <ToolPartShell part={part()} layout={props.layout} sessionId={props.sessionId} name={LOCAL_AI_LABEL} testId="turn-load-card">
      <ToolsBody row={props.row} />
    </ToolPartShell>
  );
}
