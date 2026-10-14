import { render } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { expect, it } from "vitest";
import { createTranscriptDisclosureStore, TranscriptDisclosureProvider } from "./disclosure-state.tsx";
import { TranscriptDisclosureShell, useTranscriptDisclosure } from "./transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "./transcript-disclosure-key.ts";

const CARD = transcriptDisclosureKey.tool("card");

it("keeps disclosure intent through virtual row removal and restoration", () => {
 const store=createTranscriptDisclosureStore();
 const [visible,setVisible]=createSignal(true);
 const Card=()=>{ const disclosure=useTranscriptDisclosure(()=>CARD); return <TranscriptDisclosureShell disclosure={disclosure} data-testid="card">Output</TranscriptDisclosureShell>; };
 const view=render(()=><TranscriptDisclosureProvider value={store}><Show when={visible()}><Card /></Show></TranscriptDisclosureProvider>);
 store.setUserOpen(CARD,true);
 expect(view.getByTestId("card").getAttribute("data-expanded") === "true").toBe(true);
 setVisible(false); expect(view.queryByTestId("card")).toBeNull();
 setVisible(true); expect(view.getByTestId("card").getAttribute("data-expanded") === "true").toBe(true);
});
it("releases a Find expansion while retaining an explicit user choice", () => {
 const store=createTranscriptDisclosureStore();
 const release=store.acquire(CARD);
 expect(store.isOpen(CARD)).toBe(true);
 store.setUserOpen(CARD,true); release();
 expect(store.isOpen(CARD)).toBe(true);
 store.setUserOpen(CARD,false);
 expect(store.isOpen(CARD)).toBe(false);
});
