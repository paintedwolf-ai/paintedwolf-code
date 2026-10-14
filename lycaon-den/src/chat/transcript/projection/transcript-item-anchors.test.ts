// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { transcriptItemContainsMessage, transcriptToolTarget, visibleTranscriptToolTarget } from "./transcript-item-anchors.ts";

describe("visibleTranscriptToolTarget", () => {
  it("follows nested disclosures down to the selected command as they open", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div id="outer" data-transcript-disclosure><button>Activity</button><div id="inner" data-transcript-disclosure><button>Task</button><div class="den-tool-part" data-transcript-disclosure data-tool-call-id="chosen"><button>Command</button></div></div></div>';
    const outer = root.querySelector<HTMLElement>("#outer")!;
    const inner = root.querySelector<HTMLElement>("#inner")!;
    const command = root.querySelector<HTMLElement>("[data-tool-call-id]")!;
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(outer);
    outer.setAttribute("data-expanded", String(true));
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(inner);
    inner.setAttribute("data-expanded", String(true));
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(command);
    outer.setAttribute("data-expanded", String(false));
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(outer);
  });

  it("keeps the containing card selected while its body is animating", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div id="card" data-transcript-disclosure data-expanded="true" data-animating="true"><button>Activity</button><div class="den-tool-part" data-tool-call-id="chosen"></div></div>';
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(root.querySelector("#card"));
    root.querySelector("#card")!.removeAttribute("data-animating");
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(root.querySelector("[data-tool-call-id]"));
  });
});

describe("transcriptToolTarget", () => {
  it("finds a folded diff through its row's tool-call identities", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div data-tool-call-ids="first second"><div class="den-diff-group den-transcript-disclosure-card" data-transcript-disclosure><button>1 file changed</button></div></div>';
    expect(transcriptToolTarget(root, "second")).toBe(root.querySelector(".den-diff-group"));
    expect(transcriptToolTarget(root, "sec")).toBeNull();
  });
  it("returns the visible card around a nested tool marker", () => {
    const root = document.createElement("div");
    root.innerHTML = `
      <article data-testid="task-card">
        <div data-tool-call-id="t1"></div>
      </article>
    `;

    expect(transcriptToolTarget(root, "t1")).toBe(
      root.querySelector('[data-testid="task-card"]'),
    );
  });

  it("matches tool IDs without building a selector from them", () => {
    const root = document.createElement("div");
    const card = document.createElement("div");
    card.className = "den-tool-part";
    card.dataset.toolCallId = 'call"1';
    root.append(card);

    expect(transcriptToolTarget(root, 'call"1')).toBe(card);
  });
});


it("a pending seat becomes a navigation target only after host confirmation", () => {
  const pending = { kind: "prompt" as const, operationId: "op", text: "hello", state: "sending" as const, createdAt: 1 };
  expect(transcriptItemContainsMessage({ kind: "pending_user", key: "op", text: "hello", pending }, "op")).toBe(false);
  expect(transcriptItemContainsMessage({ kind: "user", key: "op", text: "hello" }, "op")).toBe(true);
});
