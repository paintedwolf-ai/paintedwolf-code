import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, screen } from "@solidjs/testing-library";
import { MarkdownBody } from "./MarkdownBody.tsx";
import { renderMarkdownHtml } from "../../chat/markdown/markdown-render.ts";

const confirmAndOpenExternalLink = vi.hoisted(() => vi.fn().mockResolvedValue(true));
vi.mock("../../platform/desktop/external-link.ts", () => ({ confirmAndOpenExternalLink }));

describe("MarkdownBody untrusted rendering", () => {
  it("gates images in untrusted markdown — no img src on render", () => {
    const html = renderMarkdownHtml(
      "![x](https://evil.example/leak.png)",
      { untrusted: true },
    );
    expect(html).not.toMatch(/<img\b/i);
    expect(html).toContain("den-md-remote-img");
    expect(html).toContain("data-den-img-src=");
    expect(html).toContain("evil.example");
  });

  it("defers images in trusted markdown too", () => {
    const html = renderMarkdownHtml("![x](https://cdn.example/ok.png)");
    expect(html).not.toMatch(/<img\b/i);
    expect(html).toContain("den-md-remote-img");
  });

  it("surfaces full href on untrusted links", () => {
    const html = renderMarkdownHtml("[docs](https://docs.example/path?q=1)", {
      untrusted: true,
    });
    expect(html).toContain('data-tip="https://docs.example/path?q=1"');
    expect(html).toContain("den-md-href-host");
    expect(html).toContain("docs.example");
    expect(html).toContain('rel="noopener noreferrer"');
  });

  it("renders file-path links as plain text in untrusted markdown", () => {
    const html = renderMarkdownHtml("[a](src/foo.ts)", {
      untrusted: true,
      projectId: "p1",
    });
    expect(html).toContain("den-source-path-plain");
    expect(html).not.toContain("den-source-path-link");
    expect(html).not.toContain('href="src/foo.ts"');
  });

  it("opens the remote destination only through external-link confirmation", async () => {
    render(() => (
      <MarkdownBody
        untrusted
        source={"![leak](https://tracker.example/pixel.gif)"}
      />
    ));

    expect(document.querySelector("img")).toBeNull();
    const btn = screen.getByRole("button", {
      name: /image from tracker\.example/i,
    });
    expect(btn.getAttribute("data-den-img-src")).toBe(
      "https://tracker.example/pixel.gif",
    );

    fireEvent.click(btn);

    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(
      "https://tracker.example/pixel.gif",
    );
    expect(document.querySelector("img")).toBeNull();
  });
});
