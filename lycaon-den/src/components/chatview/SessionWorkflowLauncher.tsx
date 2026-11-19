import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  ICON_SLOTS,
  type IconSlot,
} from "../../contributions/theme-vocabulary.generated.ts";
import { For, Show, type JSX } from "solid-js";
import type { WorkflowSummary } from "../../api/types.ts";
import type { LauncherTile } from "../../workflow/session-launcher-model.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { NEEDS_AGENT_BADGE, WORKFLOW_NEEDS_AGENT } from "./session-mode-copy.ts";

type Props = {
  tiles: LauncherTile[];
  busy: boolean;
  askMode?: boolean;
  /** Arms a tile until the composer starts it. */
  onSelect: (workflow: WorkflowSummary) => void;
  /** When set, appends a More tile that opens the full catalog. */
  onBrowseAll?: () => void;
  heading?: string;
  armedWorkflowId?: string | null;
  testId?: string;
};

/** Unknown icon slots use the generic workflow mark. */
function Glyph(props: { name: string }): JSX.Element {
  const slot = (): IconSlot =>
    props.name in ICON_SLOTS ? (props.name as IconSlot) : "workflows";
  return <ThemeIcon slot={slot()} size={18} />;
}

export function SessionWorkflowLauncher(props: Props) {
  const heading = () => props.heading ?? "Or pick a workflow";

  return (
    <section
      class="den-session-launcher"
      data-testid={props.testId ?? "session-workflow-launcher"}
    >
      <div class="den-session-launcher__heading" {...chromeProps()}>{heading()}</div>
      <div class="den-session-launcher__grid">
        <For each={props.tiles}>
          {(tile) => {
            const needsAgent = () => props.askMode && tile.workflow.requires_agent;
            const disabled = () => Boolean(tile.disabled || needsAgent());
            return (
              <button
                type="button"
                class="den-session-launcher__tile"
                classList={{
                  "den-session-launcher__tile--disabled": disabled(),
                  "den-session-launcher__tile--armed":
                    props.armedWorkflowId === tile.workflow.id,
                }}
                disabled={disabled() || props.busy}
                aria-label={needsAgent() ? `${tile.title} — ${WORKFLOW_NEEDS_AGENT}` : tile.title}
                data-tip={needsAgent() ? `${tile.title} ${WORKFLOW_NEEDS_AGENT}.` : undefined}
                data-tip-when-clipped={needsAgent() ? "[data-needs-agent]" : undefined}
                aria-pressed={
                  props.armedWorkflowId === tile.workflow.id ? "true" : undefined
                }
                data-testid={`session-launcher-tile-${tile.workflow.id}`}
                onClick={() => props.onSelect(tile.workflow)}
              >
                <span class="den-session-launcher__tile-head">
                  <span class="den-session-launcher__tile-icon" aria-hidden="true">
                    <Glyph name={tile.icon} />
                  </span>
                  <span class="den-session-launcher__tile-title">{tile.title}</span>
                  <Show when={disabled()}>
                    <span class="den-workflow-needs-badge" data-needs-agent={needsAgent() ? "" : undefined}>
                      <Show when={needsAgent()} fallback="Needs repo">
                        {NEEDS_AGENT_BADGE}
                      </Show>
                    </span>
                  </Show>
                </span>
                <Show
                  when={tile.disabled ? tile.disabledReason : tile.description}
                  keyed
                >
                  {(text) => (
                    <span class="den-session-launcher__tile-desc">{text}</span>
                  )}
                </Show>
              </button>
            );
          }}
        </For>
        <Show when={props.onBrowseAll}>
          {(browseAll) => (
            <button
              type="button"
              class="den-session-launcher__tile den-session-launcher__tile--more"
              disabled={props.busy}
              aria-label="More"
              data-tip="Browse all workflows."
              data-tip-when-clipped=".den-session-launcher__tile-desc"
              data-testid="session-launcher-more"
              onClick={() => browseAll()()}
            >
              <span class="den-session-launcher__tile-head">
                <span class="den-session-launcher__tile-icon" aria-hidden="true">
                  <ThemeIcon slot="more" size={18} />
                </span>
                <span class="den-session-launcher__tile-title">More</span>
              </span>
              <span class="den-session-launcher__tile-desc">Browse all workflows.</span>
            </button>
          )}
        </Show>
      </div>
    </section>
  );
}
