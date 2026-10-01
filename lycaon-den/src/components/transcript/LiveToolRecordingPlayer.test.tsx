import { stubClient } from "../../test/client-fixture.ts";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { ErrorBoundary } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearVisualArtifactSrcs } from "../../chat/visual/visual-artifact-src.ts";
import { LiveToolRecordingPlayer } from "./LiveToolRecordingPlayer.tsx";

describe("LiveToolRecordingPlayer", () => {
  afterEach(() => clearVisualArtifactSrcs());

  it("loads only the recording returned by the completed upload", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(
        async () => new Blob(["mp4"], { type: "video/mp4" }),
      ),
      listSessionArtifacts: vi.fn(),
    });

    render(() => (
      <LiveToolRecordingPlayer
        client={client}
        sessionId="session-1"
        artifactId="recording-1"
      />
    ));

    await waitFor(() => {
      expect(client.getSessionArtifact).toHaveBeenCalledWith(
        "session-1",
        "recording-1",
      );
    });
    expect(client.listSessionArtifacts).not.toHaveBeenCalled();
    expect(await screen.findByTestId("live-tool-recording-player")).toBeTruthy();
  });

  it("keeps a stable media frame while the recording blob loads", async () => {
    let resolveBlob: (blob: Blob) => void = () => {};
    const client = stubClient({
      getSessionArtifact: vi.fn(
        () =>
          new Promise<Blob>((resolve) => {
            resolveBlob = resolve;
          }),
      ),
    });

    render(() => (
      <LiveToolRecordingPlayer
        client={client}
        sessionId="session-1"
        artifactId="recording-1"
      />
    ));

    const player = screen.getByTestId("live-tool-recording-player");
    expect(player.getAttribute("aria-busy")).toBe("true");
    expect(screen.getByTestId("live-tool-recording-placeholder")).toBeTruthy();
    expect(player.querySelector("video")).toBeNull();

    resolveBlob(new Blob(["mp4"], { type: "video/mp4" }));

    await waitFor(() => {
      expect(player.getAttribute("aria-busy")).toBe("false");
      expect(player.querySelector("video")).toBeTruthy();
    });
  });

  it("states the failure in the player instead of reaching the stage boundary", async () => {
    const stageFailed = vi.fn();
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => {
        throw new Error("recording 404");
      }),
    });

    render(() => (
      <ErrorBoundary
        fallback={(err) => {
          stageFailed(err);
          return <p data-testid="stage-boundary">Reload view</p>;
        }}
      >
        <LiveToolRecordingPlayer
          client={client}
          sessionId="session-1"
          artifactId="recording-gone"
        />
      </ErrorBoundary>
    ));

    await screen.findByText("Recording could not be played.");
    expect(stageFailed).not.toHaveBeenCalled();
    expect(screen.queryByTestId("stage-boundary")).toBeNull();
    expect(
      screen.getByTestId("live-tool-recording-player").getAttribute("aria-busy"),
    ).toBe("false");
  });
});
