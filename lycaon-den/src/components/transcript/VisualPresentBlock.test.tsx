import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Show, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message } from "../../api/types.ts";
import { ArtifactDedupProvider } from "../../chat/visual/artifact-dedup-context.tsx";
import { buildCanonicalArtifactPlacement } from "../../chat/visual/visual-artifact-canonical.ts";
import { resetVisualArtifactSessionMemoryForTests } from "../../chat/visual/visual-artifact-reveal.ts";
import { TranscriptVisualSlot } from "./TranscriptVisualSlot.tsx";
import { VisualPresentBlock } from "./VisualPresentBlock.tsx";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";

function pngBlob(): Blob {
  const bytes = Uint8Array.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
    0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
    0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44,
    0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x00, 0x03, 0x00,
    0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
    0x44, 0xae, 0x42, 0x60, 0x82,
  ]);
  return new Blob([bytes], { type: "image/png" });
}

async function readyThumbs(name: string, count = 1): Promise<HTMLElement[]> {
  const thumbs = await waitFor(() => {
    const found = screen.getAllByRole("button", { name });
    if (found.length !== count) {
      throw new Error(`expected ${count} thumbs named ${name}, got ${found.length}`);
    }
    for (const thumb of found) {
      if (!thumb.querySelector("img")) throw new Error("img not mounted");
    }
    return found;
  });
  for (const thumb of thumbs) {
    const img = thumb.querySelector("img");
    if (img) fireEvent.load(img);
  }
  await waitFor(() => {
    for (const thumb of thumbs) {
      expect(thumb.hasAttribute("disabled")).toBe(false);
    }
  });
  return thumbs;
}

async function readyThumb(name: string): Promise<HTMLElement> {
  const thumbs = await readyThumbs(name, 1);
  const thumb = thumbs[0];
  if (!thumb) throw new Error(`missing thumb ${name}`);
  return thumb;
}

function messagesWithFiveRefs(): Message[] {
  return [
    {
      id: "tool-1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "ok",
      ord: 1,
      created_at: "t",
      tool_result: {
        content: "ok",
        visual: {
          id: "art-shared",
          mime: "image/png",
          source: "capture",
          caption: "Shared capture",
        },
      },
    },
    {
      id: "asst-1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "one",
      ord: 2,
      created_at: "t",
      artifact_ids: ["art-shared"],
    },
    {
      id: "asst-2",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "two",
      ord: 3,
      created_at: "t",
      artifact_ids: ["art-shared"],
    },
    {
      id: "asst-3",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "three",
      ord: 4,
      created_at: "t",
      artifact_ids: ["art-shared"],
    },
    {
      id: "asst-4",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "four",
      ord: 5,
      created_at: "t",
      artifact_ids: ["art-shared"],
    },
  ];
}

describe("present strip sizing", () => {
  it("reserves the producer's wire dimensions before the image loads", () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(() => new Promise<Blob>(() => {})),
    });
    const messages: Message[] = [
      {
        id: "tool-sized",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        ord: 1,
        created_at: "t",
        tool_result: {
          content: "ok",
          visual: {
            id: "art-sized",
            mime: "image/png",
            source: "capture",
            caption: "Sized capture",
            width: 1440,
            height: 900,
          },
        },
      },
      {
        id: "asst-sized",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "see",
        ord: 2,
        created_at: "t",
        artifact_ids: ["art-sized"],
      },
    ];

    render(() => (
      <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
        <VisualPresentBlock
          artifactIds={["art-sized"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-sized"
        />
      </ArtifactDedupProvider>
    ));

    const thumb = screen.getByRole("button", { name: "Enlarge Sized capture" });
    expect(thumb.querySelector("img")).toBeNull();
    expect(thumb.style.aspectRatio).toBe("1440 / 900");
  });
});

describe("reference-as-pointer dedup", () => {
  afterEach(() => {
    resetVisualArtifactSessionMemoryForTests();
  });

  it("renders producer and first present full, then chips for later presents", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages = messagesWithFiveRefs();

    render(() => (
      <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
        <TranscriptVisualSlot
          artifact={{
            id: "art-shared",
            mime: "image/png",
            source: "capture",
            caption: "Shared capture",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-shared"
        />
        <VisualPresentBlock
          artifactIds={["art-shared"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-1"
        />
        <VisualPresentBlock
          artifactIds={["art-shared"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-2"
        />
        <VisualPresentBlock
          artifactIds={["art-shared"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-3"
        />
        <VisualPresentBlock
          artifactIds={["art-shared"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-4"
        />
      </ArtifactDedupProvider>
    ));

    await readyThumbs("Enlarge Shared capture", 2);
    expect(screen.getAllByTestId("transcript-visual-artifact")).toHaveLength(2);
    expect(screen.getAllByTestId("artifact-reference-chip")).toHaveLength(3);
    expect(document.querySelectorAll('img.den-transcript-visual__img')).toHaveLength(
      2,
    );
  });

  it("chip click scrolls to and opens the present-face lightbox", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages: Message[] = [
      {
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "first",
        ord: 1,
        created_at: "t",
        artifact_ids: ["art-nav"],
      },
      {
        id: "asst-2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "again",
        ord: 2,
        created_at: "t",
        artifact_ids: ["art-nav"],
      },
    ];
    const controller = createTranscriptViewportController({
      sessionId: () => "sess-1",
    });
    const ensureVisible = vi
      .spyOn(controller, "ensureVisible")
      .mockImplementation(() => {});

    render(() => (
      <TranscriptViewportProvider value={controller}>
        <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
          <VisualPresentBlock
            artifactIds={["art-nav"]}
            sessionId="sess-1"
            client={client}
            rowKey="asst-1"
          />
          <VisualPresentBlock
            artifactIds={["art-nav"]}
            sessionId="sess-1"
            client={client}
            rowKey="asst-2"
          />
        </ArtifactDedupProvider>
      </TranscriptViewportProvider>
    ));

    await readyThumb("Enlarge visual artifact");
    fireEvent.click(screen.getByTestId("artifact-reference-chip"));
    expect(ensureVisible).toHaveBeenCalledWith(
      expect.any(HTMLElement),
      { align: "center" },
    );
    expect(screen.getByTestId("transcript-visual-lightbox")).toBeTruthy();
  });

  it("keeps a later present compact while its canonical present row is unmounted", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages: Message[] = [
      {
        id: "asst-virtual",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "first",
        ord: 1,
        created_at: "t",
        artifact_ids: ["art-virtual"],
      },
      {
        id: "asst-virtual-2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "again",
        ord: 2,
        created_at: "t",
        artifact_ids: ["art-virtual"],
      },
    ];
    const [canonicalMounted, setCanonicalMounted] = createSignal(true);
    const revealCanonical = vi.fn(() => {
      setCanonicalMounted(true);
      return true;
    });
    Element.prototype.scrollIntoView = vi.fn();

    render(() => (
      <ArtifactDedupProvider
        placements={() => buildCanonicalArtifactPlacement(messages)}
        revealCanonical={revealCanonical}
      >
        <Show when={canonicalMounted()}>
          <VisualPresentBlock
            artifactIds={["art-virtual"]}
            sessionId="sess-1"
            client={client}
            rowKey="asst-virtual"
          />
        </Show>
        <VisualPresentBlock
          artifactIds={["art-virtual"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-virtual-2"
        />
      </ArtifactDedupProvider>
    ));

    await readyThumb("Enlarge visual artifact");
    setCanonicalMounted(false);
    await waitFor(() => {
      expect(screen.queryByTestId("transcript-visual-artifact")).toBeNull();
      expect(screen.getAllByTestId("artifact-reference-chip")).toHaveLength(1);
    });

    fireEvent.click(screen.getByTestId("artifact-reference-chip"));
    expect(revealCanonical).toHaveBeenCalledWith(
      expect.objectContaining({ rowKey: "asst-virtual", kind: "present" }),
    );
    await readyThumb("Enlarge visual artifact");
    await waitFor(() => {
      expect(screen.getByTestId("transcript-visual-lightbox")).toBeTruthy();
    });
  });

  it("first present of an id is full; a later present is a chip", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages: Message[] = [
      {
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "first",
        ord: 1,
        created_at: "t",
        artifact_ids: ["art-p"],
      },
      {
        id: "asst-2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "later",
        ord: 2,
        created_at: "t",
        artifact_ids: ["art-p"],
      },
    ];

    render(() => (
      <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
        <VisualPresentBlock
          artifactIds={["art-p"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-1"
        />
        <VisualPresentBlock
          artifactIds={["art-p"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-2"
        />
      </ArtifactDedupProvider>
    ));

    await readyThumb("Enlarge visual artifact");
    expect(screen.getAllByTestId("transcript-visual-artifact")).toHaveLength(1);
    expect(screen.getAllByTestId("artifact-reference-chip")).toHaveLength(1);
  });

  it("multi-id strip preserves order with mixed full and chip", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages: Message[] = [
      {
        id: "tool-1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        ord: 1,
        created_at: "t",
        tool_result: {
          content: "ok",
          visual: {
            id: "art-old",
            mime: "image/png",
            source: "capture",
            caption: "Old",
          },
        },
      },
      {
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "both",
        ord: 2,
        created_at: "t",
        artifact_ids: ["art-old", "art-new"],
      },
    ];

    render(() => (
      <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
        <TranscriptVisualSlot
          artifact={{
            id: "art-old",
            mime: "image/png",
            source: "capture",
            caption: "Old",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-old"
        />
        <VisualPresentBlock
          artifactIds={["art-old", "art-new"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-1"
        />
      </ArtifactDedupProvider>
    ));

    await readyThumbs("Enlarge Old", 2);
    await readyThumb("Enlarge visual artifact");
    const present = screen.getByTestId("visual-present-block");
    const kids = [...present.children];
    expect(kids).toHaveLength(2);
    expect(
      kids[0]?.querySelector("[data-testid='transcript-visual-artifact']"),
    ).toBeTruthy();
    expect(
      kids[0]
        ?.querySelector("[data-artifact-id]")
        ?.getAttribute("data-artifact-id"),
    ).toBe("art-old");
    expect(
      kids[1]?.querySelector("[data-testid='transcript-visual-artifact']"),
    ).toBeTruthy();
    expect(
      kids[1]
        ?.querySelector("[data-artifact-id]")
        ?.getAttribute("data-artifact-id"),
    ).toBe("art-new");
    expect(screen.getAllByTestId("transcript-visual-artifact")).toHaveLength(3);
    expect(screen.queryByTestId("artifact-reference-chip")).toBeNull();
  });

  it("two different ids with the same caption both render full", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages: Message[] = [
      {
        id: "tool-1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        ord: 1,
        created_at: "t",
        tool_result: {
          content: "ok",
          visual: {
            id: "art-1",
            mime: "image/png",
            source: "render",
            caption: "Twin",
          },
        },
      },
      {
        id: "tool-2",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        ord: 2,
        created_at: "t",
        tool_result: {
          content: "ok",
          visual: {
            id: "art-2",
            mime: "image/png",
            source: "render",
            caption: "Twin",
          },
        },
      },
    ];

    render(() => (
      <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
        <TranscriptVisualSlot
          artifact={{
            id: "art-1",
            mime: "image/png",
            source: "render",
            caption: "Twin",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-1"
        />
        <TranscriptVisualSlot
          artifact={{
            id: "art-2",
            mime: "image/png",
            source: "render",
            caption: "Twin",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-2"
        />
      </ArtifactDedupProvider>
    ));

    const thumbs = await screen.findAllByRole("button", {
      name: "Enlarge Twin",
    });
    expect(thumbs).toHaveLength(2);
    for (const thumb of thumbs) {
      const img = thumb.querySelector("img");
      if (img) fireEvent.load(img);
    }
    expect(screen.getAllByTestId("transcript-visual-artifact")).toHaveLength(2);
    expect(screen.queryByTestId("artifact-reference-chip")).toBeNull();
  });

  it("shows an error-outcome producer and its closeout present as two full images", async () => {
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => pngBlob()),
    });
    const messages: Message[] = [
      {
        id: "tool-ntp",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "failed",
        ord: 1,
        created_at: "t",
        tool_result: {
          content: "failed",
          outcome: "error",
          visual: {
            id: "art-ntp",
            mime: "image/png",
            source: "capture",
            caption: "Live NTP health check",
          },
        },
      },
      {
        id: "asst-ntp",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "report",
        ord: 2,
        created_at: "t",
        artifact_ids: ["art-ntp"],
      },
    ];

    render(() => (
      <ArtifactDedupProvider placements={() => buildCanonicalArtifactPlacement(messages)}>
        <TranscriptVisualSlot
          artifact={{
            id: "art-ntp",
            mime: "image/png",
            source: "capture",
            caption: "Live NTP health check",
          }}
          sessionId="sess-1"
          client={client}
          entryKey="visual:art-ntp"
        />
        <VisualPresentBlock
          artifactIds={["art-ntp"]}
          sessionId="sess-1"
          client={client}
          rowKey="asst-ntp"
        />
      </ArtifactDedupProvider>
    ));

    await readyThumbs("Enlarge Live NTP health check", 2);
    expect(screen.getAllByTestId("transcript-visual-artifact")).toHaveLength(2);
    expect(screen.queryByTestId("artifact-reference-chip")).toBeNull();
  });
});
