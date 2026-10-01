/** Transforms sanitized nodes before markup serialization. */

/** Hostname for display chips; empty when href is not an absolute http(s) URL. */
function hrefDisplayHost(href: string): string {
  const raw = href.trim();
  if (!raw) return "";
  try {
    const u = new URL(raw);
    if (u.protocol !== "http:" && u.protocol !== "https:") return "";
    return u.hostname.toLowerCase();
  } catch {
    return "";
  }
}

export type MarkdownTransformOptions = {
  /** Surface link destinations and pin rel/ping on model- or page-authored content. */
  untrusted: boolean;
};

/** Applies image deferral and link projection in place. */
export function transformSanitizedMarkdown(
  fragment: DocumentFragment,
  opts: MarkdownTransformOptions,
): void {
  deferImages(fragment);
  if (opts.untrusted) surfaceLinkDestinations(fragment);
}

/** Replaces remote images with inert buttons. */
function deferImages(fragment: DocumentFragment): void {
  const doc = fragment.ownerDocument;
  for (const img of Array.from(fragment.querySelectorAll("img"))) {
    const src = (img.getAttribute("src") ?? "").trim();
    if (!src) {
      img.remove();
      continue;
    }
    if (!/^(?:https?:)?\/\//iu.test(src)) continue;
    img.replaceWith(remoteImagePlaceholder(doc, src, img.getAttribute("alt") ?? ""));
  }
}

/** Build the inert stand-in for one deferred image. */
function remoteImagePlaceholder(
  doc: Document,
  src: string,
  alt: string,
): HTMLButtonElement {
  const host = hrefDisplayHost(src);
  const button = doc.createElement("button");
  button.type = "button";
  button.className = "den-md-remote-img";
  button.setAttribute("data-den-img-src", src);
  if (alt) button.setAttribute("data-den-img-alt", alt);
  button.dataset.tip = host ? `Open image from ${host}` : "Open remote image";

  const label = doc.createElement("span");
  label.className = "den-md-remote-img__label";
  label.textContent = host
    ? `Image from ${host} — click to open`
    : "Remote image — click to open";
  button.append(label);
  return button;
}

/** Exposes link destinations and removes tracking attributes. */
function surfaceLinkDestinations(fragment: DocumentFragment): void {
  const doc = fragment.ownerDocument;
  for (const a of Array.from(fragment.querySelectorAll("a[href]"))) {
    const href = (a.getAttribute("href") ?? "").trim();
    if (!href) continue;
    a.setAttribute("data-tip", href);
    const rel = (a.getAttribute("rel") ?? "").trim();
    if (!/\bnoopener\b/i.test(rel) || !/\bnoreferrer\b/i.test(rel)) {
      a.setAttribute("rel", "noopener noreferrer");
    }
    a.classList.add("den-external-link");
    a.removeAttribute("ping");

    const host = hrefDisplayHost(href);
    if (!host) continue;
    const next = a.nextElementSibling;
    if (next?.classList.contains("den-md-href-host") && next.textContent === host) {
      continue;
    }
    const chip = doc.createElement("span");
    chip.className = "den-md-href-host";
    chip.textContent = host;
    chip.setAttribute("data-tip", href);
    a.after(doc.createTextNode(" "), chip);
  }
}

/** Return the explicit external destination carried by a deferred image. */
export function remoteImagePlaceholderURL(el: HTMLElement): string | null {
  const src = el.getAttribute("data-den-img-src")?.trim();
  if (!src) return null;
  try {
    const url = new URL(src, document.baseURI);
    return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
  } catch {
    return null;
  }
}
