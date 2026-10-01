import type { Page } from "@playwright/test";
import type { TranscriptSpacing } from "../src/chat/transcript/layout/transcript-spacing.ts";

export async function setTranscriptSpacingForTest(page: Page, changes: Partial<TranscriptSpacing>) {
  await page.evaluate(async (changes) => {
    const path = "/src/chat/transcript/layout/transcript-spacing.ts";
    const layout = await import(/* @vite-ignore */ path) as typeof import("../src/chat/transcript/layout/transcript-spacing.ts");
    layout.setTranscriptSpacing({ ...layout.DEFAULT_TRANSCRIPT_SPACING, ...changes });
  }, changes);
}
