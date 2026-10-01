import type { Token } from "marked";
import {
  describeMarkdownBlock,
  prepareMarkdownPreview,
  type MarkdownPreviewRequest,
  type MarkdownPreviewResponse,
} from "./markdown-preview-document.ts";

let blocks: Token[][] = [];
const reply = (message: MarkdownPreviewResponse) => self.postMessage(message);

self.onmessage = (event: MessageEvent<MarkdownPreviewRequest>) => {
  try {
    const request = event.data;
    if (request.type === "prepare") {
      blocks = prepareMarkdownPreview(request.source);
      reply({ type: "ready", blocks: blocks.map(describeMarkdownBlock) });
    } else {
      reply({ type: "block", index: request.index, tokens: blocks[request.index] ?? [] });
    }
  } catch {
    reply({ type: "error" });
  }
};
