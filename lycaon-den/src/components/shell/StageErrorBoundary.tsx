import { ErrorBoundary, type JSX } from "solid-js";
import { CriticalStop } from "../CriticalStop.tsx";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";

/** Machine id for a stage whose render threw — anchors e2e and reports. */
export const STAGE_RENDER_FAILED_CODE = "VIEW_RENDER_FAILED";

type Props = {
  /** Which stage failed, for the log line. Not user copy. */
  stage: string;
  children: JSX.Element;
};

function errorFacts(err: unknown): string {
  if (err instanceof Error) {
    const name = err.name?.trim() || "Error";
    const message = err.message?.trim();
    return message ? `${name}: ${message}` : name;
  }
  return String(err);
}

/** Contains render failures to one stage. */
export function StageErrorBoundary(props: Props): JSX.Element {
  const copy = CLIENT_NOTICES.view_render_failed;
  return (
    <ErrorBoundary
      fallback={(err, reset) => {
        // Reset retries the stage.
        console.error(`[stage:${props.stage}] render failed`, err);
        return (
          <CriticalStop
            code={STAGE_RENDER_FAILED_CODE}
            title={copy.title}
            message={copy.message}
            detail={copy.suggestedAction}
            facts={errorFacts(err)}
            primaryAction={{ label: "Reload view", onClick: reset }}
          />
        );
      }}
    >
      {props.children}
    </ErrorBoundary>
  );
}
