import DOMPurify from "isomorphic-dompurify";
import { ByteCache } from "../../utils/byte-cache.ts";
import { escapeHtml } from "./html-escape.ts";
import { marked, type Token } from "marked";
import { wrapInScrollportFrame } from "../../platform/scrolling/themed-scrollbars.ts";
import { measureSync } from "../stream/den-main-thread-perf.ts";
import { transformSanitizedMarkdown } from "./untrusted-markdown.ts";
import {
  isProjectPathHref,
  renderMarkdownProjectPathLink,
} from "./markdown-project-path-link.ts";
import {
  linkifyProsePathsInText,
  navigationOccurrence,
  renderNavigationRequest,
  renderProsePathBlock,
  renderProsePathCodespan,
  resolveProsePathTarget,
  type ProseCitationIndex,
  type ProseNavigationIndex,
} from "./prose-path-opens.ts";

let configured = false;
let purifyConfigured = false;

const HTML_COMMENT_RE = /<!--[\s\S]*?-->/gu;
// XML and custom-element names remain visible prose, including unmatched closers.
const CUSTOM_MARKUP_RE = /^<\/?[a-z][a-z\d]*[-_:][\w:.-]*(?=[\s/>]|$)/iu;

/** Block fetch-capable markup outside the image policy. */
const SANITIZE_CONFIG = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: [
    "style",
    "video",
    "audio",
    "source",
    "track",
    "object",
    "embed",
    "iframe",
  ],
  FORBID_ATTR: ["style", "srcset", "ping", "formaction"],
};

/** Retain task checkboxes without resource attributes. */
function ensureDOMPurifyConfigured(): void {
  if (purifyConfigured) return;
  DOMPurify.addHook("afterSanitizeAttributes", (node) => {
    if (node.nodeName === "INPUT") {
      node.removeAttribute("src");
      node.removeAttribute("formaction");
    }
  });
  purifyConfigured = true;
}


/** Percent-encode a link href; invalid values are dropped. */
function cleanLinkHref(href: string): string {
  try {
    return encodeURI(href).replace(/%25/g, "%");
  } catch {
    return "";
  }
}

function ensureMarkedConfigured(): void {
  if (configured) return;
  marked.use({
    gfm: true,
    renderer: {
      code({ text, lang, codeBlockStyle }) {
        if (!linkProjectId || linkUntrusted) return false;
        if (lang?.trim() || codeBlockStyle === "indented") return false;
        return (
          renderProsePathBlock(text, linkProjectId, {
            index: linkCitations,
            navigation: linkNavigation,
          }) ?? false
        );
      },
      link({ href, title, text }) {
        if (isProjectPathHref(href)) {
          const occurrence = navigationOccurrence(href, "link", linkNavigation);
          const request = !linkUntrusted ? renderNavigationRequest(href, text, occurrence) : undefined;
          if (request) return request;
          const target =
            linkProjectId && !linkUntrusted
              ? resolveProsePathTarget(href, {
                  index: linkCitations,
                  navigation: occurrence,
                })
              : undefined;
          return renderMarkdownProjectPathLink(href, text, {
            projectId: linkProjectId,
            untrusted: linkUntrusted,
            requireValidatedTarget: linkRequiresValidatedProjectPaths,
            target,
          });
        }
        // Link fields can close attributes without explicit escaping.
        const cleaned = cleanLinkHref(href);
        // Invalid hrefs render as text.
        if (!cleaned) return text;
        const t = title ? ` data-tip="${escapeHtml(title)}"` : "";
        return `<a href="${escapeHtml(cleaned)}" rel="noopener noreferrer" class="den-external-link"${t}>${text}</a>`;
      },
      // Citations take precedence over navigation references.
      codespan({ text }) {
        if (!linkProjectId || linkUntrusted) return false;
        return (
          renderProsePathCodespan(text, linkProjectId, {
            index: linkCitations,
            navigation: linkNavigation,
          }) ?? false
        );
      },
      // Nested text tokens are handled by their inline children.
      text(token) {
        if (!linkProjectId || linkUntrusted) return false;
        if ("tokens" in token && token.tokens) return false;
        if (token.type === "escape") return false;
        if ("escaped" in token && token.escaped) return false;
        return (
          linkifyProsePathsInText(token.text, linkProjectId, {
            index: linkCitations,
            navigation: linkNavigation,
          }) ?? false
        );
      },
      html({ text }) {
        if (!renderHtmlLiterally && !CUSTOM_MARKUP_RE.test(text)) return false;
        return escapeHtml(text.replace(HTML_COMMENT_RE, ""));
      },
    },
  });
  configured = true;
}

let linkProjectId: string | undefined;
let linkUntrusted: boolean = false;
let linkCitations: ProseCitationIndex | undefined;
let linkNavigation: ProseNavigationIndex | undefined;
let linkRequiresValidatedProjectPaths = false;
let renderHtmlLiterally = false;

const markdownHtmlCache = new ByteCache<string, string>(8 * 1024 * 1024, 512);

export type RenderMarkdownOptions = {
  inline?: boolean;
  /** Surface destinations on untrusted links. */
  untrusted?: boolean;
  /** Project context for project-path navigation buttons. */
  projectId?: string;
  /** Cited-path index for prose path opens (citation layer). */
  citations?: ProseCitationIndex;
  /** Durable host-validated non-evidentiary project-path targets. */
  navigation?: ProseNavigationIndex;
  /** Require citation or durable host metadata before assistant path links activate. */
  requireValidatedProjectPaths?: boolean;
  /** Render HTML tokens as text and omit comments. */
  literalHtml?: boolean;
};

/** Render sanitized Markdown HTML. */
export function renderMarkdownHtml(
  source: string | Token[],
  opts?: RenderMarkdownOptions,
): string {
  const inline = opts?.inline === true;
  const untrusted = opts?.untrusted === true;
  const projectId = opts?.projectId?.trim();
  const citations = opts?.citations;
  const navigation = opts?.navigation;
  const requireValidatedProjectPaths =
    opts?.requireValidatedProjectPaths === true;
  const literalHtml = opts?.literalHtml === true;
  const cacheKey = typeof source === "string"
    ? `${untrusted ? "u" : "t"}${requireValidatedProjectPaths ? "v" : "-"}${literalHtml ? "l" : "-"}${inline ? "i" : "b"}\0${projectId ?? ""}\0${citations?.key ?? ""}\0${navigation?.key ?? ""}\0${source}`
    : undefined;
  const cached = cacheKey === undefined ? undefined : markdownHtmlCache.get(cacheKey);
  if (cached !== undefined) return cached;

  ensureMarkedConfigured();
  linkProjectId = projectId;
  linkUntrusted = untrusted;
  linkCitations = citations;
  linkNavigation = navigation ? { ...navigation, positions: new Map() } : undefined;
  linkRequiresValidatedProjectPaths = requireValidatedProjectPaths;
  renderHtmlLiterally = literalHtml;
  // Repeatedly parsing a growing stream can cost quadratic time.
  let html: string;
  try {
    html = measureSync(
      "markdown.render",
      () => {
        const raw = typeof source === "string"
          ? (inline ? marked.parseInline(source, { async: false }) : marked.parse(source, { async: false }))
          : inline ? marked.Parser.parseInline(source) : marked.parser(source);
        ensureDOMPurifyConfigured();
        // Transforming nodes before serialization preserves attribute delimiters.
        const fragment = DOMPurify.sanitize(raw, {
          ...SANITIZE_CONFIG,
          RETURN_DOM_FRAGMENT: true,
        });
        // Code and wide tables scroll sideways inside prose instead of widening it.
        for (const pre of fragment.querySelectorAll("pre")) {
          wrapInScrollportFrame(pre as HTMLElement, { frameClass: "markdown-code-scroll", axis: "x", defer: true });
        }
        for (const table of fragment.querySelectorAll("table")) {
          const extent = fragment.ownerDocument.createElement("div");
          table.replaceWith(extent);
          extent.append(table);
          wrapInScrollportFrame(extent, {
            frameClass: "markdown-table-scroll",
            axis: "x",
            defer: true,
            viewportAttributes: { tabindex: "0", role: "region", "aria-label": "Table" },
          });
        }
        transformSanitizedMarkdown(fragment, { untrusted });
        const holder = fragment.ownerDocument.createElement("div");
        holder.append(fragment);
        return holder.innerHTML;
      },
      {
        chars: typeof source === "string"
          ? source.length
          : source.reduce((total, token) => total + token.raw.length, 0),
      },
    );
  } finally {
    linkProjectId = undefined;
    linkUntrusted = false;
    linkCitations = undefined;
    linkNavigation = undefined;
    linkRequiresValidatedProjectPaths = false;
    renderHtmlLiterally = false;
  }

  if (cacheKey === undefined) return html;
  markdownHtmlCache.set(cacheKey, html, (cacheKey.length + html.length) * 2 + 64);
  return html;
}
