import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { zipAsBlob } from "../../chat/visual/filmstrip-zip.fixture.ts";
import { timelineCache } from "../../chat/visual/frame-archive-cache.ts";
import { buildTestTimelineZip } from "../../chat/visual/timeline-archive.fixture.ts";
import { TIMELINE_MIME } from "../../chat/visual/timeline-archive.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { TranscriptVisualArtifact } from "./TranscriptVisualArtifact.tsx";

function renderTimeline() {
  let created = 0;
  vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:frame-${++created}`);
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const client = stubClient({ getSessionArtifact: vi.fn(async () => zipAsBlob(buildTestTimelineZip(), TIMELINE_MIME)) });
  return render(() => (
    <TranscriptVisualArtifact
      artifact={{ id: "rec-1", mime: TIMELINE_MIME, source: "capture", caption: "Load items", width: 800, height: 600 }}
      sessionId="sess-1"
      client={client}
    />
  ));
}

describe("TranscriptVisualArtifact timeline", () => {
  afterEach(() => { timelineCache.clear(); vi.restoreAllMocks(); });

  it("shows the frame painted at the scrubbed moment", async () => {
    renderTimeline();
    const frame = await screen.findByTestId("transcript-visual-timeline-frame");
    expect(frame.getAttribute("src")).toBe("blob:frame-1");
    const scrubber = screen.getByTestId("transcript-visual-timeline-scrubber") as HTMLInputElement;
    fireEvent.input(scrubber, { target: { value: "950" } });
    await waitFor(() => expect(frame.getAttribute("src")).toBe("blob:frame-3"));
    expect(scrubber.getAttribute("aria-valuetext")).toBe("0.95s, after click #go");
    expect(frame.getAttribute("alt")).toBe("Page at 0.90s");
  });

  it("steps between painted frames with arrow keys", async () => {
    renderTimeline();
    const scrubber = await screen.findByTestId("transcript-visual-timeline-scrubber");
    fireEvent.keyDown(scrubber, { key: "ArrowRight" });
    fireEvent.keyDown(scrubber, { key: "ArrowRight" });
    expect((scrubber as HTMLInputElement).value).toBe("900");
    fireEvent.keyDown(scrubber, { key: "End" });
    expect((scrubber as HTMLInputElement).value).toBe("1500");
  });

  it("seeks to a marked event and lists what the host measured", async () => {
    renderTimeline();
    const shift = await screen.findByRole("button", { name: "Layout shift 0.180 at 0.90s" });
    fireEvent.click(shift);
    const scrubber = screen.getByTestId("transcript-visual-timeline-scrubber") as HTMLInputElement;
    expect(scrubber.value).toBe("900");
    expect(screen.getByRole("group", { name: "Actions" }).querySelectorAll("button")).toHaveLength(1);
    const facts = screen.getByTestId("transcript-visual-timeline-facts").textContent ?? "";
    expect(facts).toContain("Unexpected layout shift 0.180 (1)");
    expect(facts).toContain("1 failed request");
    fireEvent.click(screen.getByRole("button", { name: "Stable from 0.90s, show 0.90s" }));
    expect(scrubber.value).toBe("900");
  });

  it("reserves the recorded viewport before the archive decodes", () => {
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => "blob:x");
    const client = stubClient({ getSessionArtifact: vi.fn(() => new Promise<Blob>(() => {})) });
    const mounted = render(() => (
      <TranscriptVisualArtifact
        artifact={{ id: "rec-2", mime: TIMELINE_MIME, source: "capture", width: 800, height: 600 }}
        sessionId="sess-1"
        client={client}
      />
    ));
    const box = mounted.container.querySelector<HTMLElement>(".den-transcript-visual-timeline__frame");
    expect(box?.style.aspectRatio).toBe("800 / 600");
  });
});
