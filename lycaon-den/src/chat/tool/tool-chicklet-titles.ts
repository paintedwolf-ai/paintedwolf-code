/** Resolves one-line tool card subtitles from catalog metadata and task args. */

import { isDiscoveryRequest } from "./structured/discovery.ts";
import { titleArgument, truncateTitle, compositeToolTitle } from "./tool-title-format.ts";
import {
  FALLBACK_CHICKLET_TITLE_KEYS,
  TASK_DISPATCH_TOOLS,
  TOOL_CHICKLET_TITLE_KEYS,
  TOOL_TITLE_FALLBACKS,
} from "./tool-presentation.generated.ts";

const TASK_TOOL_NAMES = new Set<string>(TASK_DISPATCH_TOOLS);

const RECALL_TOOL = "recall";
const PAGE_ACT_TOOL = "page_act";

/** Returns declared keys or the catalog fallback for runtime tools. */
function chickletKeysForTool(tool: string): {
  keys: readonly string[];
  declared: boolean;
} {
  const mapped = TOOL_CHICKLET_TITLE_KEYS[tool.toLowerCase()];
  if (mapped) return { keys: mapped, declared: true };
  return { keys: FALLBACK_CHICKLET_TITLE_KEYS, declared: false };
}

/** Declared keys only, in their declared order. */
function firstStringArg(
  args: Record<string, unknown>,
  keys: readonly string[],
): string | undefined {
  for (const key of keys) {
    const fromKey = titleArgument(args[key]);
    if (fromKey) return fromKey;
  }
  return undefined;
}

function taskToolChickletTitle(
  args: Record<string, unknown>,
): string | undefined {
  const agent =
    titleArgument(args.subagent_type) ??
    titleArgument(args.agent_type);
  const brief = args.brief;
  const goal =
    brief && typeof brief === "object" && !Array.isArray(brief)
      ? titleArgument((brief as { goal?: unknown }).goal)
      : undefined;
  const desc =
    goal ??
    titleArgument(args.description) ??
    titleArgument(args.task) ??
    (typeof args.prompt === "string"
      ? truncateTitle(args.prompt.trim().split("\n")[0] ?? "")
      : undefined);
  if (agent && desc) return truncateTitle(`${agent} · ${desc}`, 96);
  if (agent) return truncateTitle(agent);
  if (desc) return truncateTitle(desc);
  return undefined;
}

/**
 * A recall past the current chat leads with its scope: the query reads the same
 * either way, and the collapsed card is the only place the reach is visible.
 */
const RECALL_WIDEN_LABELS: Record<string, string> = {
  project: "all chats in this project",
  all: "all chats, all projects",
};

function recallToolChickletTitle(
  args: Record<string, unknown>,
): string | undefined {
  const query = titleArgument(args.query);
  const widen =
    typeof args.widen === "string" ? args.widen.trim().toLowerCase() : "";
  const scope = RECALL_WIDEN_LABELS[widen];
  if (scope && query) return truncateTitle(`${scope} · ${query}`, 96);
  if (scope) return scope;
  return query ? truncateTitle(query) : undefined;
}

/**
 * A drive row reads as what it did. The page handle is minted per open, so it
 * would print a uuid; waits pad the step that mattered.
 */
function pageActChickletTitle(
  args: Record<string, unknown>,
): string | undefined {
  const steps = Array.isArray(args.actions) ? args.actions : [];
  const material: string[] = [];
  for (const step of steps) {
    if (!step || typeof step !== "object" || Array.isArray(step)) continue;
    const title = pageActStepTitle(step as Record<string, unknown>);
    if (title) material.push(title);
  }
  const first = material[0];
  if (!first) return undefined;
  const rest = material.length - 1;
  const line = rest > 0 ? `${first} +${rest} more` : first;
  return truncateTitle(args.record ? `${line} · recorded` : line, 96);
}

function pageActLocator(rec: Record<string, unknown>): string | undefined {
  return (
    titleArgument(rec.label) ??
    titleArgument(rec.text) ??
    titleArgument(rec.testid) ??
    titleArgument(rec.selector) ??
    titleArgument(rec.role)
  );
}

function pageActStepTitle(rec: Record<string, unknown>): string | undefined {
  const type = typeof rec.type === "string" ? rec.type.trim() : "";
  switch (type) {
    case "":
    case "wait":
    case "wait_for":
      return undefined;
    case "press":
      return `press ${titleArgument(rec.key) ?? ""}`.trim();
    case "drag": {
      const from = pageActLocator(rec);
      const to = rec.to && typeof rec.to === "object" ? pageActLocator(rec.to as Record<string, unknown>) : undefined;
      return [`drag${from ? ` ${from}` : ""}`, to].filter(Boolean).join(" → ");
    }
    case "scroll":
      return `scroll ${pageActLocator(rec) ?? "page"}`;
    case "route": {
      const routes = Array.isArray(rec.routes) ? rec.routes : [];
      const first = routes[0] && typeof routes[0] === "object" ? titleArgument((routes[0] as Record<string, unknown>).url) : undefined;
      if (!first) return "route";
      return routes.length > 1 ? `route ${first} +${routes.length - 1}` : `route ${first}`;
    }
    case "type":
      return `type ${titleArgument(rec.value) ?? ""}`.trim();
    default: {
      const target = pageActLocator(rec) ?? titleArgument(rec.value);
      return target ? `${type} ${target}` : type;
    }
  }
}

/** Resolves a one-line subtitle from a tool call. */
export function resolveChickletTitle(
  tool: string,
  args?: Record<string, unknown>,
): string | undefined {
  args ??= {};
  if (isDiscoveryRequest(tool, args)) return "Available catalog";
  if (TASK_TOOL_NAMES.has(tool.toLowerCase())) {
    return taskToolChickletTitle(args);
  }
  if (tool.toLowerCase() === RECALL_TOOL) {
    return recallToolChickletTitle(args);
  }
  if (tool.toLowerCase() === PAGE_ACT_TOOL) {
    return pageActChickletTitle(args);
  }
  const composite = compositeToolTitle(tool.toLowerCase(), args);
  if (composite !== null) return composite ? truncateTitle(composite) : undefined;
  const { keys, declared } = chickletKeysForTool(tool);
  const primary = firstStringArg(args, keys);
  if (primary) return truncateTitle(primary);
  // Declared tools use only their named title arguments.
  if (declared) return TOOL_TITLE_FALLBACKS[tool.toLowerCase()];
  const loose = titleArgument(args.description);
  return loose ? truncateTitle(loose) : undefined;
}
