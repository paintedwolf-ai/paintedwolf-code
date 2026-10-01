import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import {
  indexWarmingChickletTitle,
  indexWarmingSkipNote,
  indexWarmingToolName,
  indexWarmingTopicNote,
  indexWarmingTriggerDetail,
  indexWarmingTriggerPhrase,
} from "../../chat/transcript/projection/index-warming-copy.ts";
import { IndexWarmingChicklet } from "./IndexWarmingChicklet.tsx";

describe("IndexWarmingChicklet", () => {
  it("uses the tool chicklet shell with a done status dot", () => {
    const { container } = render(() => (
      <IndexWarmingChicklet
        layout="chat"
        meta={{
          trigger: "declared_url",
          tier: "crawl",
          topic: "docs.example",
          hosts: ["docs.example", "news.example"],
          pages: 3,
        }}
      />
    ));
    const chicklet = container.querySelector('[data-testid="index-warming-chicklet"]');
    expect(chicklet?.classList.contains("den-tool-part")).toBe(true);
    expect(container.querySelector(".den-tool-part-name")?.textContent).toBe(
      indexWarmingToolName,
    );
    expect(
      container.querySelector(".den-tool-part-status-dot")?.getAttribute("data-status"),
    ).toBe("done");
  });

  it("does not show topic text in the collapsed title", () => {
    const { container } = render(() => (
      <IndexWarmingChicklet
        layout="chat"
        meta={{
          trigger: "search",
          tier: "crawl",
          topic: "tailwind dark mode",
          hosts: ["tailwindcss.com"],
          pages: 2,
        }}
      />
    ));
    const title = container.querySelector(".den-tool-part-title")?.textContent ?? "";
    expect(title).toContain("after web_search");
    expect(title).toContain("1 host");
    expect(title).not.toContain("tailwind dark mode");
  });

  it("prefers crawl stats over seed skip_reason in the title", () => {
    const { container } = render(() => (
      <IndexWarmingChicklet
        layout="chat"
        meta={{
          trigger: "declared_url",
          tier: "crawl",
          topic: "tailwind dark mode",
          hosts: ["tailwindcss.com"],
          pages: 2,
          skip_reason: "hourly seed cap",
        }}
      />
    ));
    const title = container.querySelector(".den-tool-part-title")?.textContent ?? "";
    expect(title).toContain("prefetch for shared link");
    expect(title).toContain("1 host");
    expect(title).not.toContain("hourly seed cap");
    expect(title).not.toContain("tailwind dark mode");
    expect(container.textContent).toContain(
      indexWarmingSkipNote("hourly seed cap"),
    );
  });

  it("explains declared-URL warms and site topic in the expanded body", () => {
    const { container } = render(() => (
      <IndexWarmingChicklet
        layout="chat"
        meta={{
          trigger: "declared_url",
          tier: "crawl",
          topic: "widget frobnicator",
          pages: 1,
        }}
      />
    ));
    container
      .querySelector('[data-testid="index-warming-chicklet"]')
      ?.setAttribute("open", "open");
    expect(container.textContent).toContain(indexWarmingTriggerDetail("declared_url"));
    expect(container.textContent).toContain(
      indexWarmingTopicNote("widget frobnicator", "declared_url"),
    );
  });

  it("explains fetch-triggered warms in the expanded body", () => {
    const { container } = render(() => (
      <IndexWarmingChicklet
        layout="chat"
        meta={{
          trigger: "fetch",
          tier: "crawl",
          topic: "Widget guide",
          pages: 1,
        }}
      />
    ));
    container
      .querySelector('[data-testid="index-warming-chicklet"]')
      ?.setAttribute("open", "open");
    expect(container.textContent).toContain(indexWarmingTriggerDetail("fetch"));
  });
});

describe("indexWarmingChickletTitle", () => {
  it("covers all session warm triggers without topic snippets", () => {
    expect(indexWarmingTriggerPhrase("intent")).toBe("background prefetch");
    expect(indexWarmingTriggerPhrase("search")).toBe("after web_search");
    expect(indexWarmingTriggerPhrase("fetch")).toBe("after fetch_url");
    expect(
      indexWarmingChickletTitle({
        trigger: "fetch",
        pages: 1,
        topic: "Widget guide",
      }),
    ).toBe("after fetch_url · 0 hosts, 1 page");
  });
});
