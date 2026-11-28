import { EditorView } from "@codemirror/view";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BriefingRecord, BriefingRecordItem } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";
import { LycaonApiError } from "../api/http.ts";
import type { ChatContentDocument } from "../chat/transcript/content/chat-content-document.ts";
import { BriefingEvidencePage } from "./BriefingEvidencePage.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { briefingReturnAnchor, openBriefingEvidence } from "./briefing-evidence.ts";

const openFilesSurface = vi.hoisted(() => vi.fn());
vi.mock("../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  openFilesSurface.mockClear();
});

const copy = briefingCopy.evidence;
const hash = (letter: string) => letter.repeat(64);

function result(section_id: string, text: string, source: "summarizer" | "deterministic") {
  return { section_id, text, manifest_hash: hash("a"), source, reused: false, provider_id: "provider", model: "model", evidence_quality: [], created_at: "2026-11-11T10:00:00Z" };
}

const item = (key: string, inclusion: BriefingRecordItem["inclusion"], text?: string, reason?: string): BriefingRecordItem =>
  ({ key, role: "context", source_kind: "message", inclusion, ...(text === undefined ? {} : { text }), ...(reason ? { reason } : {}) });

const record: BriefingRecord = {
  record_id: hash("r"), profile_id: "tool_activity", profile_version: "1", method: "mixed", created_at: "2026-11-11T10:00:00Z",
  sections: [
    {
      section_id: "purpose", manifest_hash: hash("a"), available: true, template_id: "action_purpose", template_version: "2",
      result: result("purpose", "Ships the fix.", "summarizer"),
      items: [
        item("user_intent", "included", "SUPPLIED_INTENT"),
        item("recent_results", "truncated", "[{\"tool\":\"command\",\"outco"),
        item("tracked_plan", "unavailable", undefined, "absent"),
        item("diffs", "omitted", undefined, "too large"),
      ],
    },
    {
      section_id: "outcome", manifest_hash: hash("b"), available: true, template_id: "activity_outcome", template_version: "1",
      result: result("outcome", "Completed", "deterministic"),
      items: [item("state", "included", "Completed")],
    },
    { section_id: "change", manifest_hash: hash("c"), available: false },
  ],
};

function client(get: BriefingsClient["getBriefingRecord"]): BriefingsClient {
  return { requestBriefing: async () => { throw new Error("unused"); }, getBriefingRecord: get };
}

async function shown(value: BriefingRecord = record): Promise<HTMLElement> {
  render(() => <BriefingEvidencePage projectId="p" recordId={value.record_id} client={client(async () => value)} />);
  const page = await screen.findByTestId("briefing-evidence-page");
  await waitFor(() => expect(page.querySelector('[data-testid="briefing-evidence-section"]')).toBeTruthy());
  return page;
}

/** The text each editor block under an element holds. */
const editorTexts = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[data-testid="briefing-evidence-text"] .cm-editor')]
  .map((editor) => EditorView.findFromDOM(editor)?.state.doc.toString());

const section = (page: HTMLElement, id: string) => page.querySelector<HTMLElement>(`[data-testid="briefing-evidence-section"][data-section="${id}"]`)!;
const inputKeys = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[data-testid="briefing-evidence-input"]')].map((input) => input.dataset.key);

/** Stands in for layout: bodies report the given content height against a fixed held height. */
function stubLayout(contentHeight: (body: HTMLElement) => number) {
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} unobserve() {} });
  const body = (el: Element) => el.classList.contains("den-briefing-evidence__body");
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockImplementation(function (this: HTMLElement) {
    return body(this) ? (this.hasAttribute("data-expanded") ? contentHeight(this) : Math.min(100, contentHeight(this))) : 0;
  });
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return body(this) ? contentHeight(this) : 0;
  });
}

describe("BriefingEvidencePage", () => {
  it("shows every sent input once under its own section and only names the rest", async () => {
    const page = await shown();
    expect(inputKeys(section(page, "purpose"))).toEqual(["user_intent", "recent_results"]);
    expect(inputKeys(section(page, "outcome"))).toEqual(["state"]);
    expect(inputKeys(section(page, "change"))).toEqual([]);
    expect(inputKeys(page)).toHaveLength(3);
    expect(section(page, "purpose").textContent).toContain("SUPPLIED_INTENT");
    // Sentences are page text; only literal inputs are editors.
    expect(editorTexts(section(page, "purpose").querySelector<HTMLElement>('[data-key="user_intent"]')!)).toEqual([]);
    expect(within(page.parentElement!.closest(".den-briefing-evidence-frame") as HTMLElement).getByTestId("files-editor-line-layout")).toBeTruthy();

    const purpose = within(section(page, "purpose"));
    expect(purpose.getByTestId("briefing-evidence-nothing-to-send").textContent).toBe(copy.nothingToSend(copy.inputNames.tracked_plan!.toLowerCase()));
    expect(purpose.getByTestId("briefing-evidence-not-sent").textContent).toBe(copy.notSent(`${copy.inputNames.diffs!.toLowerCase()} (too large)`));
    expect(within(section(page, "outcome")).queryByTestId("briefing-evidence-nothing-to-send")).toBeNull();
    expect(section(page, "change").textContent).toContain(copy.unavailableSection);
  });

  it("shows a cut input exactly as stored and says it was sent in part", async () => {
    const page = await shown();
    const cut = section(page, "purpose").querySelector<HTMLElement>('[data-key="recent_results"]')!;
    expect(cut.dataset.inclusion).toBe("truncated");
    expect(editorTexts(cut)).toEqual([record.sections[0]!.items![1]!.text]);
    expect(cut.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    expect(cut.textContent).toContain(copy.sentInPart);
  });

  it("counts inputs by how the section was made and marks only written lines", async () => {
    const page = await shown();
    expect(section(page, "purpose").querySelector("h2")?.textContent).toContain(copy.writtenFrom(2));
    expect(section(page, "outcome").querySelector("h2")?.textContent).toContain(copy.computedFrom(1));
    const lines = [...within(page).getByTestId("briefing-evidence-summary").querySelectorAll<HTMLElement>(".den-activity-summary__section")];
    expect(lines.map((line) => [line.querySelector(".den-briefing-summary__label")?.textContent, !!line.querySelector(".den-briefing-mark")])).toEqual([
      [`${copy.sectionNames.tool_activity!.purpose}:`, true],
      [`${copy.sectionNames.tool_activity!.outcome}:`, false],
    ]);
  });

  it("gives a single-section summary one line per paragraph and shows unlisted names as identifiers", async () => {
    const single: BriefingRecord = {
      ...record, record_id: hash("s"), profile_id: "later_profile" as BriefingRecord["profile_id"], method: "model",
      sections: [{
        section_id: "digest", manifest_hash: hash("a"), available: true, template_id: "t", template_version: "1",
        result: result("digest", "First paragraph.\n\nSecond paragraph.", "summarizer"),
        items: [item("later_key", "included", "VALUE")],
      }],
    };
    const page = await shown(single);
    const lines = within(page).getByTestId("briefing-evidence-summary").querySelectorAll(".den-activity-summary__text");
    expect([...lines].map((line) => line.textContent?.trim())).toEqual(["First paragraph.", "Second paragraph."]);
    expect(section(page, "digest").querySelector("h2")?.textContent).toContain("digest");
    expect(section(page, "digest").querySelector(".den-walk-page__name")?.textContent).toBe("later_key");
  });

  it("captions each stored result, and counts a section made neither way by its inputs alone", async () => {
    const results = JSON.stringify([{ tool: "command", outcome: "completed", content: "ok" }, { tool: "read", outcome: "failed", content: "no" }]);
    const other: BriefingRecord = {
      ...record, record_id: hash("f"),
      sections: [{
        section_id: "purpose", manifest_hash: hash("a"), available: true, template_id: "t", template_version: "1",
        result: { ...result("purpose", "Held text.", "summarizer"), source: "fallback" },
        items: [item("recent_results", "included", results)],
      }, record.sections[1]!],
    };
    const page = await shown(other);
    expect(section(page, "purpose").querySelector("h2 .den-walk-page__count")?.textContent).toBe(copy.inputCount(1));
    const input = section(page, "purpose").querySelector<HTMLElement>('[data-key="recent_results"]')!;
    expect([...input.querySelectorAll(".den-briefing-evidence__part-head")].map((head) => head.textContent)).toEqual(["commandcompleted", "readfailed"]);
    expect(editorTexts(input)).toEqual(["ok", "no"]);
    expect(input.querySelector(".den-briefing-evidence__item-head")?.textContent).toContain(copy.resultCount(2));
  });

  it("shows every input whole with no control where layout cannot be observed", async () => {
    vi.stubGlobal("ResizeObserver", undefined);
    const page = await shown();
    expect(inputKeys(page)).toHaveLength(3);
    expect(page.querySelectorAll('[data-testid="briefing-evidence-show-all"]')).toHaveLength(0);
    expect(page.querySelector(".den-briefing-evidence__body[data-clamped]")).toBeNull();
  });

  it("offers Show all only on an input taller than its held height; it grows in place and can be held again", async () => {
    // 400 is far past the held height of 100; 140 is past it by less than the slack; 40 is under it.
    stubLayout((body) => (body.textContent?.includes("SUPPLIED_INTENT") ? 400 : body.textContent?.includes("Completed") ? 140 : 40));
    const page = await shown();
    const controls = () => [...page.querySelectorAll<HTMLElement>('[data-testid="briefing-evidence-show-all"]')];
    await waitFor(() => expect(controls()).toHaveLength(1));
    const long = section(page, "purpose").querySelector<HTMLElement>('[data-key="user_intent"]')!;
    expect(long.contains(controls()[0]!)).toBe(true);
    expect(long.querySelector(".den-briefing-evidence__body")?.hasAttribute("data-clamped")).toBe(true);
    // Text a little taller than the held height is shown whole, with no control.
    const nearly = section(page, "outcome").querySelector<HTMLElement>('[data-key="state"] .den-briefing-evidence__body')!;
    expect(nearly.hasAttribute("data-expanded")).toBe(true);
    expect(nearly.hasAttribute("data-clamped")).toBe(false);

    fireEvent.click(controls()[0]!);
    const body = long.querySelector(".den-briefing-evidence__body")!;
    expect(body.hasAttribute("data-expanded")).toBe(true);
    expect(body.hasAttribute("data-clamped")).toBe(false);
    expect(controls().map((control) => control.getAttribute("aria-expanded"))).toEqual(["true"]);

    // A grown input can be held again; however large it is, the page gets its length back.
    fireEvent.click(controls()[0]!);
    expect(body.hasAttribute("data-expanded")).toBe(false);
    expect(body.hasAttribute("data-clamped")).toBe(true);
    expect(controls().map((control) => control.getAttribute("aria-expanded"))).toEqual(["false"]);
    expect(openFilesSurface).not.toHaveBeenCalled();
  });

  it("opens tabs only from the activity's own records, and has no records row without them", async () => {
    const page = await shown();
    expect(within(page).queryByTestId("briefing-evidence-records")).toBeNull();
    expect(page.querySelectorAll('[data-testid="briefing-evidence-section"] button')).toHaveLength(0);
    page.remove();

    const documents = ["Input", "Receipt"].map((label): ChatContentDocument => ({
      kind: "tool", sessionId: "s", toolCallId: "call", messageId: "m", pane: `action:part:${label}`, title: label, content: { kind: "inline", text: label },
    }));
    const linked = { ...record, record_id: hash("l") };
    openBriefingEvidence({ project_id: "p", record_id: linked.record_id }, undefined, documents.map((document) => ({ label: document.title, document })));
    openFilesSurface.mockClear();
    const withRecords = await shown(linked);
    const links = within(within(withRecords).getByTestId("briefing-evidence-records")).getAllByTestId("briefing-evidence-record");
    expect(links).toHaveLength(2);
    fireEvent.click(links[1]!);
    expect(openFilesSurface).toHaveBeenCalledExactlyOnceWith({ kind: "chat-content", projectId: "p", document: documents[1] });
  });

  it("says when a record no longer exists", async () => {
    const missing = client(async () => {
      throw new LycaonApiError("gone", 404, "briefing_record_not_found");
    });
    render(() => <BriefingEvidencePage projectId="p" recordId="gone" client={missing} />);
    await screen.findByText(copy.expired);
    expect(screen.queryByTestId("briefing-evidence-section")).toBeNull();
  });

  it("remembers bounded return anchors and focuses the caller on Back", async () => {
    for (let i = 0; i < 66; i++) {
      openBriefingEvidence({ project_id: "p", record_id: `rec-${i}` }, { label: `card-${i}`, focus: () => {} });
    }
    expect(briefingReturnAnchor("rec-0")).toBeUndefined();
    expect(briefingReturnAnchor("rec-65")?.label).toBe("card-65");

    const focus = vi.fn();
    const returned = { ...record, record_id: hash("b") };
    openBriefingEvidence({ project_id: "p", record_id: returned.record_id }, { label: "activity", focus });
    render(() => <BriefingEvidencePage projectId="p" recordId={returned.record_id} client={client(async () => returned)} />);
    const back = await screen.findByRole("button", { name: copy.back("activity") });
    fireEvent.click(back);
    expect(focus).toHaveBeenCalledOnce();
  });
});
