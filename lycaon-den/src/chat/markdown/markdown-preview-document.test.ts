import { describe, expect, it } from "vitest";
import { marked } from "marked";
import { prepareMarkdownPreview, describeMarkdownBlock } from "./markdown-preview-document.ts";
import { prepareMarkdownSource } from "./markdown-output.ts";
import { renderMarkdownHtml } from "./markdown-render.ts";

describe("file preview document", () => {
  it("preserves cross-block references, fences, nested lists, tables and HTML", () => {
    const source = `# Title\n\n[Reference][target]\n\n${"A paragraph.\n\n".repeat(700)}\n- Parent\n  - **Child**\n\n\`\`\`ts\nconst text = '<tag>';\n\`\`\`\n\n| Key | Value |\n| --- | --- |\n| A | B |\n\n<details><summary>More</summary>Body</details>\n\n[target]: https://example.com/reference\n`;
    const blocks = prepareMarkdownPreview(source);
    expect(blocks.length).toBeGreaterThan(2);
    expect(blocks.map((tokens) => marked.parser(tokens)).join("")).toBe(
      marked.parse(prepareMarkdownSource(source)),
    );
    expect(renderMarkdownHtml(blocks[0]!)).toContain('href="https://example.com/reference"');
  });

  it("sanitizes worker tokens and retains deferred images and code copy markup", () => {
    const [tokens] = prepareMarkdownPreview('<script>alert(1)</script>\n\n![Remote](https://example.com/image.png)\n\n```js\nalert(1)\n```');
    const html = renderMarkdownHtml(tokens!);
    expect(html).not.toContain("<script");
    expect(html).not.toContain("<img");
    expect(html).toContain("den-md-remote-img");
    expect(html).toContain('<code class="language-js">');
  });

  it("indexes a multi-megabyte document without a document-sized render block", () => {
    const source = ("## Package\n\nApache license\n\n```text\n" + "Permission is granted.\n".repeat(100) + "```\n\n").repeat(1100);
    const blocks = prepareMarkdownPreview(source);
    const sizes = blocks.map(describeMarkdownBlock);
    expect(source.length).toBeGreaterThan(2_000_000);
    expect(blocks.length).toBeGreaterThan(500);
    expect(Math.max(...sizes.map((block) => block.chars))).toBeLessThan(32_768);
    expect(blocks.flat().map((token) => token.raw).join("")).toContain("Apache");
  });
});
