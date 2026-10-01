// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { renderMarkdownHtml } from "./markdown-render.ts";
import { remoteImagePlaceholderURL } from "./untrusted-markdown.ts";

function render(source: string): HTMLDivElement {
  const host = document.createElement("div");
  host.innerHTML = renderMarkdownHtml(source, { untrusted: true });
  document.body.append(host);
  return host;
}

function placeholderFor(src: string): HTMLElement {
  const host = render(`<p>x</p><img src="${src}" alt="a chart">`);
  const el = host.querySelector(".den-md-remote-img");
  if (!(el instanceof HTMLElement)) throw new Error("invariant: no placeholder rendered");
  return el;
}

describe("click-to-open images", () => {
  it("renders a placeholder that makes no request", () => {
    const el = placeholderFor("https://img.test/a.png");
    expect(el.getAttribute("data-den-img-src")).toBe("https://img.test/a.png");
    expect(el.closest("div")?.querySelector("img")).toBeNull();
  });

  it("returns the confirmed external destination without creating an image", () => {
    const el = placeholderFor("https://img.test/a.png");
    const parent = el.parentElement;
    expect(remoteImagePlaceholderURL(el)).toBe("https://img.test/a.png");
    expect(parent?.querySelector("img")).toBeNull();
  });

  it("keeps local data images renderable", () => {
    const host = render(`<img src="data:image/png;base64,AA==" alt="local">`);
    expect(host.querySelector("img")?.getAttribute("src")).toBe("data:image/png;base64,AA==");
    expect(host.querySelector(".den-md-remote-img")).toBeNull();
  });

  it("ignores a placeholder with no source", () => {
    const el = document.createElement("button");
    el.className = "den-md-remote-img";
    expect(remoteImagePlaceholderURL(el)).toBeNull();
  });
});

describe("markup smuggled through an attribute value", () => {
  const cases: Array<{ name: string; source: string; forbidden: string }> = [
    {
      name: "an image alias in a Markdown image title",
      source: `![a](https://ok.test/x.png "t><image src=https://evil.test/pixel.png>")`,
      forbidden: "img[src*='evil.test']",
    },
    {
      name: "an image alias in an alt attribute",
      source: `<img alt="q><image src=https://evil.test/pixel.png>" src="https://ok.test/x.png">`,
      forbidden: "img[src*='evil.test']",
    },
    {
      name: "an event handler on a tag the sanitizer forbids",
      source: `<img alt="x><svg onload=alert(1)>" src="https://ok.test/x.png">`,
      forbidden: "svg",
    },
    {
      name: "a frame the sanitizer forbids",
      source: `<img alt="x><iframe src=javascript:alert(1)>" src="https://ok.test/x.png">`,
      forbidden: "iframe",
    },
    {
      name: "a handler that fires without script, in a link title",
      source: `[t](https://ok.test/ "a><details open ontoggle=alert(1)>")`,
      forbidden: "details",
    },
  ];

  for (const tc of cases) {
    it(`keeps ${tc.name} as text`, () => {
      const host = render(tc.source);
      expect(host.querySelector(tc.forbidden)).toBeNull();
      expect(host.querySelector("[onload]")).toBeNull();
      expect(host.querySelector("[ontoggle]")).toBeNull();
      expect(host.querySelector("img")).toBeNull();
    });
  }

  it("still defers the image the payload rode in on", () => {
    const host = render(
      `![a](https://ok.test/x.png "t><image src=https://evil.test/pixel.png>")`,
    );
    const button = host.querySelector(".den-md-remote-img");
    expect(button?.getAttribute("data-den-img-src")).toBe("https://ok.test/x.png");
  });

  it("defers an <image> alias written directly", () => {
    const host = render(`<image src="https://evil.test/pixel.png">`);
    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector(".den-md-remote-img")?.getAttribute("data-den-img-src")).toBe(
      "https://evil.test/pixel.png",
    );
  });
});

describe("untrusted link destinations", () => {
  it("shows the host and pins rel", () => {
    const host = render(`[click](https://dest.test/path)`);
    const link = host.querySelector("a");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link?.getAttribute("data-tip")).toBe("https://dest.test/path");
    expect(host.querySelector(".den-md-href-host")?.textContent).toBe("dest.test");
  });

  it("drops a ping destination", () => {
    const host = render(`<a href="https://dest.test/" ping="https://tracker.test/p">t</a>`);
    expect(host.querySelector("a")?.hasAttribute("ping")).toBe(false);
  });
});
