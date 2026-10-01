import { For, Show } from "solid-js";
import type { ContributionSearchResult, ContributionSearchSource } from "../../api/types.ts";
import { type SearchMatchPrefs } from "../../search/search-match-prefs.ts";
import type { ContributionCommand } from "../../api/types.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { evaluateCondition } from "../../contributions/conditions.ts";
import { liveShellFactLookup } from "../../contributions/shell-facts.ts";
import { SearchKindIcon } from "./SearchIcons.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

export function commandDisabledReason(command: ContributionCommand): string {
  const fact = firstBlockingConditionFact(command.enablement);
  if (fact?.fact === "mcp_requirement_ready" && fact.is) {
    const requirement = contributionFrame()?.requirements.find((row) => row.id === fact.is);
    switch (requirement?.reason) {
      case "provider_missing": return "Provider is not installed";
      case "provider_disabled": return "Provider is disabled";
      case "provider_not_ready": return "Provider needs configuration";
      case "tool_missing": return "Required provider tool is unavailable";
    }
  }
  const labels: Record<string, string> = {
    project_open: "Open a project to run this command",
    session_exists: "Open a chat to run this command",
    session_idle: "Wait for the chat to become idle",
    editor_active: "Open a file to run this command",
    editor_has_selection: "Select text to run this command",
    editor_has_symbol: "Place the caret on a symbol",
    editor_has_finding: "Select a recorded finding",
  };
  return labels[fact?.fact ?? ""] ?? "Not available in the current context";
}

export function ContributionSearchLane(props: {
  source: ContributionSearchSource;
  query: string;
  results: ContributionSearchResult[];
  error: string;
  searching: boolean;
  activeIndex: number;
  onHover: (index: number) => void;
  onActivate: (result: ContributionSearchResult) => void;
  onConfigure: () => void;
}) {
  const status = () => {
    if (props.error) return props.error;
    if (props.query.length < props.source.min_query_length) return `Type at least ${props.source.min_query_length} characters`;
    if (props.searching) return "Searching…";
    if (props.results.length === 0) return "No results";
    return "";
  };
  return (
    <Scrollport
      class="den-crossbar__list"
      contentAs="ul"
      contentClass="den-crossbar__list-content"
      data-testid="contribution-search-results"
    >
      <li class="den-crossbar__section">From {props.source.provider} / {props.source.label}</li>
      <Show when={status()}><li class="den-crossbar__status" role="status">{status()}</li></Show>
      <Show when={!props.source.ready}>
        <li class="den-crossbar__status"><button type="button" onClick={props.onConfigure}>Open provider settings</button></li>
      </Show>
      <For each={props.results}>{(result, index) => <li>
        <button
          type="button"
          class="den-crossbar__row"
          classList={{ "den-crossbar__row--active": index() === props.activeIndex }}
          data-testid="contribution-search-result"
          onMouseEnter={() => props.onHover(index())}
          onClick={() => props.onActivate(result)}
        >
          <span class="den-crossbar__glyph"><SearchKindIcon kind={result.kind || "item"} /></span>
          <span class="den-crossbar__primary">{result.title}</span>
          <Show when={result.description}><span class="den-crossbar__secondary">{result.description}</span></Show>
          <span class="den-crossbar__chip">From {props.source.label}</span>
        </button>
        <Show when={result.detail}><div class="den-crossbar__source-detail">{result.detail}</div></Show>
      </li>}</For>
    </Scrollport>
  );
}

export function sourceDisabledLabel(source: ContributionSearchSource): string {
  switch (source.disabled_reason) {
    case "provider_missing": return "Provider is not installed";
    case "provider_disabled": return "Provider is disabled";
    case "provider_not_ready": return "Provider needs configuration";
    case "tool_missing": return "Required provider tool is unavailable";
    case "tool_not_read_only": return "Provider search tool is not declared read-only";
    default: return "Source is not ready";
  }
}

function firstBlockingConditionFact(condition: ContributionCommand["enablement"]): { fact?: string; is?: string } | null {
  if (!condition) return null;
  const lookup = liveShellFactLookup();
  if (!lookup) return null;
  if (condition.fact) return evaluateCondition(condition, lookup) ? null : { fact: condition.fact, is: condition.is };
  if (condition.not) return null;
  for (const child of condition.all ?? condition.any ?? []) {
    if (evaluateCondition(child, lookup)) continue;
    const found = firstBlockingConditionFact(child);
    if (found) return found;
  }
  return null;
}

export function globIndicatorTitle(prefs: SearchMatchPrefs): string {
  const parts: string[] = [];
  if (prefs.include.trim()) parts.push(`include ${prefs.include.trim()}`);
  if (prefs.exclude.trim()) parts.push(`exclude ${prefs.exclude.trim()}`);
  return `Path filters from full search: ${parts.join(", ")}. Click to clear.`;
}

export function renderMatchUnderline(text: string, indexes: readonly number[]) {
  if (indexes.length === 0) return text;
  const mark = new Set(indexes);
  const parts: Array<{ text: string; match: boolean }> = [];
  let buf = "";
  let matching = mark.has(0);
  for (let i = 0; i < text.length; i++) {
    const isMatch = mark.has(i);
    if (isMatch !== matching && buf) {
      parts.push({ text: buf, match: matching });
      buf = "";
    }
    matching = isMatch;
    buf += text[i];
  }
  if (buf) parts.push({ text: buf, match: matching });
  return (
    <>
      <For each={parts}>
        {(p) => (
          <Show when={p.match} fallback={p.text}>
            <span class="den-crossbar__match">{p.text}</span>
          </Show>
        )}
      </For>
    </>
  );
}
