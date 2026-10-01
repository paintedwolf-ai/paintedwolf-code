import type { ContextNavItemId } from "../../../shared/app-state-types.ts";

/** Starting layout page of the first-run gate. */
export const ONBOARDING_LAYOUT_COPY = {
  title: "How do you want to work?",
  lede: "Pick what opens when you start the app. You can switch views anytime.",
  chatName: "Chat",
  chatDescription:
    "The conversation fills the window. Files open when the work needs them.",
  splitName: (stageLabel: string) => `${stageLabel} and chat`,
  splitDescription: (stageId: ContextNavItemId, stageLabel: string) =>
    stageId === "files"
      ? "Your project's files and editor stay open beside the conversation."
      : `${stageLabel} stays open beside the conversation.`,
  settingsHint: "Change this later in Settings › General › Layout.",
} as const;
