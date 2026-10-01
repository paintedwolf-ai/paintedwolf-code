import { createSignal } from "solid-js";
import { render, cleanup, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import {
  StageErrorBoundary,
  STAGE_RENDER_FAILED_CODE,
} from "./StageErrorBoundary.tsx";

describe("StageErrorBoundary", () => {
  afterEach(cleanup);

  it("renders its stage untouched when nothing throws", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <StageErrorBoundary stage="chat">
        <div data-testid="stage-body">transcript</div>
      </StageErrorBoundary>
    ));

    expect(getByTestId("stage-body").textContent).toBe("transcript");
    expect(queryByTestId("critical-stop")).toBeNull();
  });

  it("replaces a stage whose render throws, and logs the failure", () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    const { getByTestId } = render(() => (
      <StageErrorBoundary stage="chat">
        {(() => {
          throw new Error("workflow run missing");
        })()}
      </StageErrorBoundary>
    ));

    const stop = getByTestId("critical-stop");
    expect(stop.getAttribute("data-code")).toBe(STAGE_RENDER_FAILED_CODE);
    // Copy comes from the client-notice catalog, never the call site.
    expect(stop.textContent).toContain(CLIENT_NOTICES.view_render_failed.title);
    expect(getByTestId("critical-stop-detail").textContent).toBe(
      CLIENT_NOTICES.view_render_failed.suggestedAction,
    );
    // The message a user would quote in a report survives to the screen.
    expect(getByTestId("critical-stop-facts").textContent).toContain(
      "workflow run missing",
    );
    expect(logged).toHaveBeenCalled();
    logged.mockRestore();
  });

  it("restarts the stage when the cause is gone", () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    const [broken, setBroken] = createSignal(true);

    const { getByTestId, queryByTestId } = render(() => (
      <StageErrorBoundary stage="chat">
        {(() => {
          if (broken()) throw new Error("transient");
          return <div data-testid="stage-body">recovered</div>;
        })()}
      </StageErrorBoundary>
    ));

    expect(queryByTestId("stage-body")).toBeNull();
    setBroken(false);
    fireEvent.click(getByTestId("critical-stop-primary"));

    expect(getByTestId("stage-body").textContent).toBe("recovered");
    expect(queryByTestId("critical-stop")).toBeNull();
    logged.mockRestore();
  });
});
