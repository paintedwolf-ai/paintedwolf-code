import { render } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { expect, it } from "vitest";
import { createTranscriptDisclosureStore, TranscriptDisclosureProvider } from "./disclosure-state.tsx";
import { useTranscriptDisclosure } from "./transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "./transcript-disclosure-key.ts";

const CARD = transcriptDisclosureKey.tool("card");

it("keeps disclosure intent through virtual row removal and restoration", () => {
 const store=createTranscriptDisclosureStore();
 const [visible,setVisible]=createSignal(true);
 const Card=()=>{ const disclosure=useTranscriptDisclosure(()=>CARD); return <details open={disclosure.open()}><summary>Tool</summary>Output</details>; };
 const view=render(()=><TranscriptDisclosureProvider value={store}><Show when={visible()}><Card /></Show></TranscriptDisclosureProvider>);
 store.setUserOpen(CARD,true);
 expect(view.container.querySelector("details")?.open).toBe(true);
 setVisible(false); expect(view.container.querySelector("details")).toBeNull();
 setVisible(true); expect(view.container.querySelector("details")?.open).toBe(true);
});
it("keeps open state and leases when a surface's key changes", () => {
 const before=transcriptDisclosureKey.activitySpan("call-b");
 const after=transcriptDisclosureKey.activitySpan("call-a");
 const store=createTranscriptDisclosureStore();
 store.setUserOpen(before,true);
 store.rename(before,after);
 expect(store.isOpen(before)).toBe(false);
 expect(store.isOpen(after)).toBe(true);
 expect(store.userOpenKeys()).toEqual([after]);
 const release=store.acquire(before);
 store.setUserOpen(after,false);
 store.rename(before,after);
 expect(store.isOpen(after)).toBe(true);
 release();
 expect(store.isOpen(after)).toBe(false);
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
