import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { clearArtifactDeletionMemory, notifyArtifactChanged } from "../../chat/visual/artifact-change-store.ts";
import { clearVisualArtifactSessionMemory } from "../../chat/visual/visual-artifact-reveal.ts";
import { visualArtifactSrcSync } from "../../chat/visual/visual-artifact-src.ts";
import { FILMSTRIP_MIME } from "../../chat/visual/filmstrip-zip.ts";
import { buildTestFilmstripZip, zipAsBlob } from "../../chat/visual/filmstrip-zip.fixture.ts";
import { TranscriptVisualArtifact } from "./TranscriptVisualArtifact.tsx";
import { LiveToolRecordingPlayer } from "./LiveToolRecordingPlayer.tsx";
import { ArtifactReferenceChip } from "./ArtifactReferenceChip.tsx";
import { FeedbackArtifactThumb } from "../workflow/WorkflowFeedbackArtifacts.tsx";

afterEach(() => {
  clearVisualArtifactSessionMemory();
  clearArtifactDeletionMemory();
  vi.restoreAllMocks();
});

describe("artifact deletion across media lifecycles", () => {
  it("updates direct recordings, workflow previews and reference chips together", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    const client = stubClient({ getSessionArtifact: vi.fn(async () => new Blob(["synthetic"], { type: "video/mp4" })) });
    const view = render(() => <>
      <LiveToolRecordingPlayer client={client} sessionId="session" artifactId="shared-artifact" />
      <FeedbackArtifactThumb client={client} sessionId="session" artifactId="shared-artifact" />
      <ArtifactReferenceChip artifactId="shared-artifact" face="present" caption="Shared artifact" />
    </>);
    await waitFor(() => expect(view.container.querySelectorAll("video")).toHaveLength(2));
    notifyArtifactChanged({ project_id: "project", artifact_id: "shared-artifact", op: "deleted" });
    await waitFor(() => expect(view.container.querySelectorAll("video")).toHaveLength(0));
    expect(screen.getAllByText("This artifact was deleted.")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Shared artifact — deleted" }).hasAttribute("disabled")).toBe(true);
    expect(revoke).toHaveBeenCalledTimes(2);
    clearVisualArtifactSessionMemory();
    expect(screen.getAllByText("This artifact was deleted.")).toHaveLength(2);
    view.unmount();
  });

  for (const mime of ["image/png", FILMSTRIP_MIME, "video/mp4"]) {
    it.each(["pending", "loaded"])(`${mime} retires %s media and preserves its reference`, async (phase) => {
      let release!: (blob: Blob) => void;
      const fetch = vi.fn(() => new Promise<Blob>((resolve) => { release = resolve; }));
      let next = 0;
      const create = vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:delete-${++next}`);
      const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
      const client = stubClient({ getSessionArtifact: fetch });
      const mount = () => render(() => <TranscriptVisualArtifact
        artifact={{ id: "deleted-media", mime, source: "capture", caption: "Synthetic media" }}
        sessionId="session" projectId="project" client={client} />);
      const view = mount();
      const blob = mime === FILMSTRIP_MIME
        ? zipAsBlob(buildTestFilmstripZip(), mime) : new Blob(["synthetic"], { type: mime });
      if (phase === "loaded") {
        release(blob);
        await waitFor(() => expect(view.container.querySelector("img,video")).not.toBeNull());
        if (mime !== "video/mp4") {
          const image = view.container.querySelector("img");
          if (image) fireEvent.load(image);
          const button = view.container.querySelector("button");
          if (button) fireEvent.click(button);
          expect(screen.getByRole("dialog")).toBeTruthy();
        }
      }
      notifyArtifactChanged({ project_id: "project", artifact_id: "unrelated", op: "deleted" });
      notifyArtifactChanged({ project_id: "project", artifact_id: "deleted-media", op: "written" });
      expect(screen.queryByText("This artifact was deleted.")).toBeNull();
      notifyArtifactChanged({ project_id: "project", artifact_id: "deleted-media", op: "deleted" });
      expect(screen.getByText("This artifact was deleted.")).toBeTruthy();
      expect(view.container.querySelector("img,video")).toBeNull();
      expect(screen.queryByRole("dialog")).toBeNull();
      if (phase === "pending") release(blob);
      if (phase === "pending" && mime === FILMSTRIP_MIME) {
        await fetch.mock.results[0]!.value;
        expect(create).not.toHaveBeenCalled();
      } else await waitFor(() => expect(create).toHaveBeenCalled());
      await waitFor(() => expect(revoke.mock.calls.map(([src]) => src).sort())
        .toEqual(create.mock.results.map((result) => result.value).sort()));
      expect(visualArtifactSrcSync("session", "deleted-media")).toBeUndefined();
      view.unmount();
      const remount = mount();
      expect(screen.getByText("This artifact was deleted.")).toBeTruthy();
      expect(fetch).toHaveBeenCalledTimes(1);
      remount.unmount();
    });

    it(`${mime} reads a tombstone after a fresh connection`, async () => {
      render(() => <><TranscriptVisualArtifact
        artifact={{ id: "old-deletion", mime, source: "capture" }} sessionId="session"
        client={stubClient({ getSessionArtifact: vi.fn(async () => {
          throw new LycaonApiError("Removed", 410, "artifact_deleted");
        }) })} /><ArtifactReferenceChip artifactId="old-deletion" face="present" caption="Old artifact" /></>);
      expect(await screen.findByText("This artifact was deleted.")).toBeTruthy();
      expect(screen.getByRole("button", { name: "Old artifact — deleted" }).hasAttribute("disabled")).toBe(true);
    });
  }
});
