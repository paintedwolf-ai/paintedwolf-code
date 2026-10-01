import { tauriDragRegionProps } from "../../platform/runtime.ts";

/** Shared wording keeps workspace handoffs visually continuous. */
export const PROJECT_WORKSPACE_LOADING_HINT = "Opening workspace…";

type Props = {
  label?: string;
  hint?: string;
  /** Errors replace the busy state with an alert. */
  tone?: "waiting" | "error";
};

export function ProjectLoadingStage(props: Props) {
  const label = () => props.label?.trim() || "Project";
  const failed = () => props.tone === "error";

  return (
    <section
      class="project-loading-stage"
      data-testid="project-loading-stage"
      aria-busy={!failed()}
      aria-live="polite"
      // Text descendants remain draggable.
      {...tauriDragRegionProps({ deep: true })}
    >
      <p class="project-loading-stage__title">{label()}</p>
      <p
        class="project-loading-stage__hint"
        classList={{ "project-loading-stage__hint--error": failed() }}
        role={failed() ? "alert" : undefined}
      >
        {props.hint ?? PROJECT_WORKSPACE_LOADING_HINT}
      </p>
    </section>
  );
}
