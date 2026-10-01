import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import type { IconSlot } from "../../contributions/theme-vocabulary.generated.ts";
import { Show, type JSX } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";

export type WorkerSectionKey = "task" | "activity" | "coordination" | "evidence";

export type WorkerSectionStatus =
  | "working"
  | "complete"
  | "partial"
  | "failed"
  | "pending";

const SECTION_STATUS_LABEL: Record<WorkerSectionStatus, string> = {
  working: "Working",
  complete: "Complete",
  partial: "Partial",
  failed: "Failed",
  pending: "Pending",
};

function SectionStatusGlyph(props: { status: WorkerSectionStatus }) {
  return <ThemeIcon slot={statusSlot(props.status)} size={14} />;
}

function statusSlot(status: WorkerSectionStatus): IconSlot {
  switch (status) {
    case "working":
      return "busy";
    case "complete":
      return "check";
    case "partial":
    case "failed":
      return "high-risk";
    default:
      return "pending";
  }
}

type Props = {
  title: string;
  sectionKey?: string;
  onSelect: () => void;
  id?: string;
  class?: string;
  classList?: Record<string, boolean | undefined>;
  status?: WorkerSectionStatus;
  "data-testid"?: string;
  "data-evidence-outcome"?: string;
  bodyRef?: (element: HTMLElement) => void;
  /** The body is loading more of its content. */
  busy?: boolean;
  children: JSX.Element;
};

export function WorkerTranscriptSection(props: Props) {
  return (
    <div
      id={props.id}
      class={`den-worker-transcript-section ${props.class ?? ""}`}
      classList={{ ...props.classList }}
      data-section={props.sectionKey}
      data-testid={props["data-testid"]}
      data-evidence-outcome={props["data-evidence-outcome"]}
    >
      <button
        type="button"
        class="den-worker-transcript-section-summary"
        onClick={() => props.onSelect()}
        {...chromeProps()}
      >
        <h3 class="transcript-pane__subhead">{props.title}</h3>
        <Show when={props.status} keyed>
          {(status) => (
            <span
              class={`den-worker-transcript-section-status den-worker-transcript-section-status--${status}`}
              data-testid="worker-section-status"
              data-status={status}
              data-tip={SECTION_STATUS_LABEL[status]}
              aria-label={SECTION_STATUS_LABEL[status]}
              role="img"
            >
              <SectionStatusGlyph status={status} />
            </span>
          )}
        </Show>
      </button>
      <div class="den-worker-transcript-section-body" ref={props.bodyRef} aria-busy={props.busy || undefined}>
        {props.children}
      </div>
    </div>
  );
}
