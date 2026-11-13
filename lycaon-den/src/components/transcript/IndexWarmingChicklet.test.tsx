import { disclosureToggle, expectSameCollapsedFace, rowSpokenLabel, rowVisibleText } from "../../test/transcript-disclosure-fixture.ts";
import { fireEvent, render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { IndexWarmingMeta } from "../../api/types.ts";
import {
  indexWarmingChickletTitle,
  indexWarmingSkipNote,
  indexWarmingStatsLine,
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

  it("keeps the summary off the row and topic text out of the spoken label", () => {
    const meta: IndexWarmingMeta = { trigger: "search", tier: "crawl", topic: "tailwind dark mode", hosts: ["tailwindcss.com"], pages: 2 };
    const { container } = render(() => <IndexWarmingChicklet layout="chat" meta={meta} />);
    const card = container.querySelector('[data-testid="index-warming-chicklet"]');
    const title = rowSpokenLabel(card);
    expect(rowVisibleText(card)).not.toContain(indexWarmingChickletTitle(meta));
    expect(title).toContain(indexWarmingChickletTitle(meta));
    expect(title).toContain("after web_search");
    expect(title).toContain("1 host");
    expect(title).not.toContain("tailwind dark mode");
  });

  it("prefers crawl stats over seed skip_reason in the spoken label", () => {
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
    const card = container.querySelector('[data-testid="index-warming-chicklet"]');
    const title = rowSpokenLabel(card);
    expect(title).toContain("1 host");
    expect(title).not.toContain("hourly seed cap");
    expect(title).not.toContain("tailwind dark mode");
    expect(container.textContent).toContain(
      indexWarmingSkipNote("hourly seed cap"),
    );
  });

  it("states its outcome on the row and leads the opened body with its counts", () => {
    const meta: IndexWarmingMeta = { trigger: "fetch", tier: "crawl", hosts: ["docs.example", "news.example"], pages: 3 };
    const finished = render(() => <IndexWarmingChicklet layout="chat" meta={meta} entryKey="warm-finished" sessionId="warming-test" />);
    const card = finished.container.querySelector('[data-testid="index-warming-chicklet"]');
    const outcome = card?.querySelector('[data-testid="tool-part-completion"]')?.textContent ?? "";
    expect(outcome).not.toBe("");
    expect(rowSpokenLabel(card)).toContain(outcome);
    fireEvent.click(disclosureToggle(card));
    expect(card?.querySelector(".den-tool-part-card-note")?.textContent).toBe(indexWarmingStatsLine(meta));
    const skipped = render(() => <IndexWarmingChicklet layout="chat" meta={{ ...meta, skip_reason: "discovery busy" }} />);
    expect(skipped.container.querySelector('[data-testid="tool-part-completion"]')?.textContent).not.toBe(outcome);
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

describe("IndexWarmingChicklet under the WebKitGTK AT-SPI workaround", () => {
  it("shows the same collapsed card", async () => {
    await expectSameCollapsedFace(() => {
      const view = render(() => (
        <IndexWarmingChicklet
          layout="chat"
          meta={{ trigger: "declared_url", tier: "crawl", topic: "docs.example", hosts: ["docs.example", "news.example"], pages: 3, skip_reason: "hourly seed cap" }}
        />
      ));
      return { card: () => view.container.querySelector('[data-testid="index-warming-chicklet"]'), unmount: view.unmount };
    });
  });
});
