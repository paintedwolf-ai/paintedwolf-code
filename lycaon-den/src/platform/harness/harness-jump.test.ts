// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { switchSession, type JumpDeps } from "./harness-jump.ts";

afterEach(() => { document.body.replaceChildren(); vi.unstubAllGlobals(); });

it("selects a stage's other chat, then activates that selected row to show the conversation", async () => {
  vi.stubGlobal("CSS", { escape: (value: string) => value });
  document.body.innerHTML = '<div data-testid="focused-session-list"><button data-session-id="next">Next chat</button></div>';
  let row = document.querySelector("button")!;
  let revealed = false;
  const click = vi.fn(() => {
    if (row.getAttribute("aria-current") === "true") revealed = true;
    else {
      const selected = row.cloneNode(true) as HTMLButtonElement;
      selected.setAttribute("aria-current", "true");
      selected.addEventListener("click", click);
      row.replaceWith(selected);
      row = selected;
    }
  });
  row.addEventListener("click", click);
  const deps: JumpDeps = {
    readState: () => ({ sessionId: revealed ? "next" : null }),
    waitForSettledSession: async () => ({ ok: revealed }),
    liveByTestid: () => revealed ? row : null,
    liveResolve: () => null,
    waitForIdle: async () => ({ ok: true }),
    sendPrompt: async () => ({ ok: true }),
    llmManual: async () => ({ ok: true }),
    llmRespond: async () => ({ ok: true }),
  };
  expect(await switchSession(deps, "next")).toMatchObject({ ok: true, state: { sessionId: "next" } });
  expect(click).toHaveBeenCalledTimes(2);
});
