import type { Token } from "marked";
import { renderMarkdownHtml } from "../../chat/markdown/markdown-render.ts";

/** Clear of the clipped window, so the probe never paints. */
const PROBE_OFFSET_PX = -1_000_000;

export type MarkdownBlockSizer = {
  /** Rendered height of one block in the box that will paint it. */
  measure: (tokens: Token[]) => number | null;
  release: () => void;
};

/**
 * Sizes preview blocks inside the window that paints them, so an unpainted
 * block contributes its real height to the scroll extent.
 */
export function createMarkdownBlockSizer(
  window: () => HTMLElement | undefined,
  projectId: () => string,
): MarkdownBlockSizer {
  let probe: HTMLDivElement | undefined;
  let body: HTMLDivElement | undefined;

  const attach = (): HTMLDivElement | undefined => {
    const host = window();
    if (!host) return undefined;
    if (body && probe?.parentElement === host) return body;
    probe?.remove();
    probe = document.createElement("div");
    // The block class carries the width and margin containment of a real block.
    probe.className = "den-markdown-preview-block";
    probe.setAttribute("aria-hidden", "true");
    probe.style.top = `${PROBE_OFFSET_PX}px`;
    probe.style.visibility = "hidden";
    probe.style.pointerEvents = "none";
    body = document.createElement("div");
    body.className = "markdown-body";
    probe.appendChild(body);
    host.appendChild(probe);
    return body;
  };

  return {
    measure(tokens) {
      const target = attach();
      if (!target || !probe) return null;
      target.innerHTML = renderMarkdownHtml(tokens, { projectId: projectId() });
      const height = probe.getBoundingClientRect().height;
      return Number.isFinite(height) && height > 0 ? height : null;
    },
    release() {
      probe?.remove();
      probe = undefined;
      body = undefined;
    },
  };
}
