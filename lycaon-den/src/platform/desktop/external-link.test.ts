// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  confirmAndOpenExternalLink,
  externalLinkConfirmMessage,
  externalLinkOkLabel,
  isExternalLinkHref,
  openAppLink,
  isAppLink,
  openInBrowser,
  setupWebviewNavigationGuards,
} from "./external-link.ts";
import { REPOSITORY_URL, WEBSITE_URL } from "../../../shared/brand.ts";
import { resetExternalOpenPrefsForTests } from "../../settings/editor/external-open-prefs.ts";

describe("external-link", () => {
  it("accepts site and repository descendants while refusing lookalikes", () => {
    for (const url of [`${WEBSITE_URL}/news`, `${REPOSITORY_URL}/releases/tag/v1.0.1`]) expect(isAppLink(url)).toBe(true);
    for (const url of ["https://paintedwolf.ai.evil.test", `${REPOSITORY_URL}-evil`, "http://paintedwolf.ai", "https://user@paintedwolf.ai", "https://paintedwolf.ai:444/news"]) expect(isAppLink(url)).toBe(false);
  });

  beforeEach(() => {
    vi.restoreAllMocks();
    resetExternalOpenPrefsForTests();
  });

  it("recognizes external schemes only", () => {
    expect(isExternalLinkHref("https://example.com")).toBe(true);
    expect(isExternalLinkHref("http://127.0.0.1:8080")).toBe(true);
    expect(isExternalLinkHref("mailto:hi@example.com")).toBe(true);
    expect(isExternalLinkHref("tel:+15551212")).toBe(true);
    expect(isExternalLinkHref("./relative.md")).toBe(false);
    expect(isExternalLinkHref("javascript:alert(1)")).toBe(false);
    expect(isExternalLinkHref("#section")).toBe(false);
  });

  it("includes the unvalidated warning in confirm copy", () => {
    expect(externalLinkConfirmMessage("https://example.com")).toContain(
      "has not been validated",
    );
    expect(externalLinkConfirmMessage("https://example.com")).toContain(
      "https://example.com",
    );
  });

  it("names the browser in the OK label when preset ≠ system-default", () => {
    expect(externalLinkOkLabel("https://example.com", "system-default")).toBe("Open in browser");
    expect(externalLinkOkLabel("https://example.com", "chrome")).toBe("Open in Google Chrome");
    expect(externalLinkOkLabel("https://example.com", "firefox")).toBe("Open in Firefox");
    expect(externalLinkOkLabel("https://example.com", "safari")).toBe("Open in Safari");
  });

  it("opens via openInBrowser after confirm in web dev", async () => {
    const openUrl = vi.fn();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);

    const opened = await confirmAndOpenExternalLink(
      "https://example.com/path",
      {
        openInBrowser: async (url, opts) => {
          await openInBrowser(url, { ...opts, openUrl, isTauri: false });
        },
      },
    );

    expect(opened).toBe(true);
    expect(confirmSpy).toHaveBeenCalledOnce();
    expect(openUrl).toHaveBeenCalledWith("https://example.com/path");
  });

  it("does not open when the user cancels — no bypass", async () => {
    const openSpy = vi.fn();
    vi.spyOn(window, "confirm").mockReturnValue(false);

    const opened = await confirmAndOpenExternalLink("https://example.com", {
      openInBrowser: openSpy,
    });

    expect(opened).toBe(false);
    expect(openSpy).not.toHaveBeenCalled();
  });

  it("routes confirm OK label from browser prefs", async () => {
    resetExternalOpenPrefsForTests({ browser: "chrome" });
    const confirm = vi.fn().mockResolvedValue(true);
    const openSpy = vi.fn().mockResolvedValue(undefined);

    await confirmAndOpenExternalLink("https://example.com", {
      confirm,
      openInBrowser: openSpy,
    });

    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining("https://example.com"),
      "Open in Google Chrome",
    );
    expect(openSpy).toHaveBeenCalledWith("https://example.com", {
      browser: "chrome",
    });
  });

  it("uses the default application for mail and phone links", async () => {
    resetExternalOpenPrefsForTests({ browser: "chrome" });
    const open = vi.fn();
    const confirm = vi.fn(async () => true);
    await confirmAndOpenExternalLink("mailto:hi@example.com", { confirm, openInBrowser: open });
    expect(confirm).toHaveBeenCalledWith(expect.any(String), "Open in default application");
    expect(open).toHaveBeenCalledWith("mailto:hi@example.com", { browser: "system-default" });
  });

  it("always confirms before openInBrowser (INV-SRC-08)", async () => {
    const order: string[] = [];
    await confirmAndOpenExternalLink("https://example.com", {
      confirm: async () => {
        order.push("confirm");
        return true;
      },
      openInBrowser: async () => {
        order.push("open");
      },
    });
    expect(order).toEqual(["confirm", "open"]);
  });

  it("opens the app's own links without confirmation, in the preferred browser", async () => {
    resetExternalOpenPrefsForTests({ browser: "chrome" });
    const confirmSpy = vi.spyOn(window, "confirm");
    const open = vi.fn().mockResolvedValue(undefined);
    expect(await openAppLink(REPOSITORY_URL, { openInBrowser: open })).toBe(true);
    expect(confirmSpy).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith(REPOSITORY_URL, { browser: "chrome" });
  });

  it("refuses to open anything but the app's own links without confirmation", async () => {
    const open = vi.fn();
    for (const url of ["https://example.com", `${REPOSITORY_URL}/../evil`, `${WEBSITE_URL}.evil.test`, ` ${WEBSITE_URL}`]) {
      expect(await openAppLink(url, { openInBrowser: open })).toBe(false);
    }
    expect(open).not.toHaveBeenCalled();
  });

  describe("navigation guards", () => {
    beforeEach(() => {
      window.open = vi.fn(() => null) as typeof window.open;
      setupWebviewNavigationGuards();
    });

    it("intercepts external anchor clicks", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);

      document.body.innerHTML =
        '<div class="markdown-body"><a href="https://example.com">Go</a></div>';
      document.querySelector("a")!.dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true }),
      );

      await Promise.resolve();
      expect(confirmSpy).toHaveBeenCalledOnce();
    });

    it("opens external anchors while other text stays selected", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
      document.body.innerHTML =
        '<p>Earlier message</p><div class="markdown-body"><a href="https://example.com">Go</a></div>';
      document.getSelection()!.selectAllChildren(document.querySelector("p")!);
      const click = new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 });
      try {
        document.querySelector("a")!.dispatchEvent(click);
        await Promise.resolve();
        expect(click.defaultPrevented).toBe(true);
        expect(confirmSpy).toHaveBeenCalledOnce();
      } finally {
        document.getSelection()!.removeAllRanges();
      }
    });

    it("keeps the webview in place when a click ends selecting an anchor's text", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
      document.body.innerHTML =
        '<div class="markdown-body"><a href="https://example.com">Go</a></div>';
      const anchor = document.querySelector("a")!;
      document.getSelection()!.selectAllChildren(anchor);
      const click = new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 });
      try {
        anchor.dispatchEvent(click);
        await Promise.resolve();
        expect(click.defaultPrevented).toBe(true);
        expect(confirmSpy).not.toHaveBeenCalled();
      } finally {
        document.getSelection()!.removeAllRanges();
      }
    });

    it("blocks window.open and routes external URLs through confirm", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);

      const popup = window.open("https://example.com/popup");

      expect(popup).toBeNull();
      await Promise.resolve();
      expect(confirmSpy).toHaveBeenCalledOnce();
    });

    it("leaves handled application forms in the app", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
      document.body.innerHTML = '<form><input name="branch"><button type="submit">Create branch</button></form>';
      const form = document.querySelector("form")!;
      const submit = vi.fn((event: Event) => event.preventDefault());
      form.addEventListener("submit", submit);
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await Promise.resolve();
      expect(submit).toHaveBeenCalledOnce();
      expect(confirmSpy).not.toHaveBeenCalled();
    });

    it("intercepts external form submissions", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);

      document.body.innerHTML =
        '<form action="https://example.com/post"><button type="submit">Send</button></form>';
      document.querySelector("form")!.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );

      await Promise.resolve();
      expect(confirmSpy).toHaveBeenCalledOnce();
    });
  });
});
