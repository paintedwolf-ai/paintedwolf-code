import { stubClient } from "../../test/client-fixture.ts";
import { render, screen } from "@solidjs/testing-library";
import { ErrorBoundary } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { FeedbackArtifactThumb } from "./WorkflowFeedbackArtifacts.tsx";

/**
 * Ask thumbs render inside the Chat stage, whose only error boundary replaces the
 * whole stage. A refused artifact fetch must stay inside the thumb — the dock's
 * unsent answer lives above it.
 */
describe("FeedbackArtifactThumb refused fetch", () => {
  it("states the failure in the thumb instead of reaching the stage boundary", async () => {
    const stageFailed = vi.fn();
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => {
        throw new Error("artifact 404");
      }),
    });

    render(() => (
      <ErrorBoundary
        fallback={(err) => {
          stageFailed(err);
          return <p data-testid="stage-boundary">Reload view</p>;
        }}
      >
        <FeedbackArtifactThumb
          client={client}
          sessionId="sess-1"
          artifactId="art-gone"
          label="A"
        />
      </ErrorBoundary>
    ));

    await screen.findByText("Preview unavailable.");
    expect(stageFailed).not.toHaveBeenCalled();
    expect(screen.queryByTestId("stage-boundary")).toBeNull();
    expect(screen.getByTestId("workflow-feedback-artifact")).toBeTruthy();
  });

  it("keeps a selectable thumb answerable when its preview never loads", async () => {
    const onSelect = vi.fn();
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => {
        throw new Error("artifact 404");
      }),
    });

    render(() => (
      <FeedbackArtifactThumb
        client={client}
        sessionId="sess-1"
        artifactId="art-gone"
        label="B"
        selectable
        radioName="ask-1"
        onSelect={onSelect}
      />
    ));

    await screen.findByText("Preview unavailable.");
    const radio = screen.getByLabelText("B") as HTMLInputElement;
    expect(radio.disabled).toBe(false);
  });
});
