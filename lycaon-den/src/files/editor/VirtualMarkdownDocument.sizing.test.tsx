import { afterEach, describe, expect, it } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { Lexer } from "marked";
import { describeMarkdownBlock } from "../../chat/markdown/markdown-preview-document.ts";
import { VirtualMarkdownDocument, type PreviewDocument } from "./VirtualMarkdownDocument.tsx";

/** Heights a real layout reports; the char-and-line estimate cannot predict them. */
const BLOCK_HEIGHTS = [420, 900, 260, 1180, 505];
const BLOCK_GAP_PX = 14;

const nativeRect = HTMLElement.prototype.getBoundingClientRect;

/** Gives the off-screen probe the height of the block it currently holds. */
function measureProbesAs(heights: readonly number[]): void {
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    const isProbe =
      this.classList.contains("den-markdown-preview-block") &&
      this.getAttribute("aria-hidden") === "true";
    if (!isProbe) return nativeRect.call(this);
    const index = Number(/block-(\d+)/.exec(this.textContent ?? "")?.[1] ?? NaN);
    const box = nativeRect.call(this);
    return new DOMRect(box.x, box.y, box.width, heights[index] ?? 0);
  };
}

function renderDocument() {
  const blocks = BLOCK_HEIGHTS.map((_, index) => Lexer.lex(`block-${index}\n`));
  const reads: number[] = [];
  const document_: PreviewDocument = {
    blocks: blocks.map(describeMarkdownBlock),
    ready: () => {},
    read: (index, receive) => {
      reads.push(index);
      receive(blocks[index] ?? []);
      return () => {};
    },
  };
  const scrollport = document.createElement("div");
  // The test DOM has no scrolling; the virtualizer still calls for it.
  scrollport.scrollTo = () => {};
  document.body.appendChild(scrollport);
  render(
    () => (
      <VirtualMarkdownDocument
        document={document_}
        projectId="project"
        scrollport={() => scrollport}
      />
    ),
    { container: scrollport },
  );
  const window = () =>
    scrollport.querySelector<HTMLElement>(".den-markdown-preview-window");
  const probes = () =>
    scrollport.querySelectorAll(".den-markdown-preview-block[aria-hidden='true']");
  return { scrollport, reads, window, probes };
}

afterEach(() => {
  HTMLElement.prototype.getBoundingClientRect = nativeRect;
  document.body.replaceChildren();
});

describe("markdown preview scroll extent", () => {
  it("sizes every block off-screen, so the extent never moves under the reader", async () => {
    measureProbesAs(BLOCK_HEIGHTS);
    const { window, probes } = renderDocument();
    const settled =
      BLOCK_HEIGHTS.reduce((sum, height) => sum + height, 0) +
      BLOCK_GAP_PX * (BLOCK_HEIGHTS.length - 1);

    await waitFor(() => expect(window()?.style.height).toBe(`${settled}px`));
    // Nothing is left for a later measurement to correct.
    await waitFor(() => expect(probes()).toHaveLength(0));
  });

  it("stops sizing when the box reports no layout rather than reading every block", async () => {
    measureProbesAs([]);
    const { window, reads, probes } = renderDocument();
    await waitFor(() => expect(window()?.style.height).toBeTruthy());
    await waitFor(() => expect(probes()).toHaveLength(0));

    const settled = [...new Set(reads)];
    expect(settled.length).toBeLessThan(BLOCK_HEIGHTS.length);
    expect(Number.parseFloat(window()!.style.height)).toBeGreaterThan(0);
  });
});
