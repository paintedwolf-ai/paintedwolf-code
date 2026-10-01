export type FirstTimeTipPlacement = "top" | "right" | "bottom" | "left";

type FirstTimeTip = {
  priority: number;
  preferredPlacement: FirstTimeTipPlacement;
  title: string;
  body: string;
};

/** Catalog ids are durable anchor ids. */
export const FIRST_TIME_TIPS = {
  "files-ai-editor": {
    priority: 10,
    preferredPlacement: "right",
    title: "Files is a full code editor",
    body: "Open, edit, search, and review project files here. It is designed for working alongside AI without leaving your workspace.",
  },
  "files-project-roots": {
    priority: 30,
    preferredPlacement: "left",
    title: "Project roots organize your files",
    body: "Each root is a folder included in this project. Browse its tree to work with its files; the primary root is where project-level work begins.",
  },
  "files-review-scope": {
    priority: 40,
    preferredPlacement: "bottom",
    title: "Choose what to compare",
    body: "Use this eye to choose where a comparison starts: what’s new since you looked, this turn, the whole chat, a commit, or a saved snapshot. It always reads the selected chat.",
  },
  "review-seen": {
    priority: 45,
    preferredPlacement: "right",
    title: "Files you’ve seen move here",
    body: "Once you’ve looked at a file, its changes stop counting as new. Open it again for clean current text. Choose View reviewed changes to revisit that review, or mark it unseen to bring it back.",
  },
  "project-search": {
    priority: 60,
    preferredPlacement: "bottom",
    title: "Search connects code and project work",
    body: "Search starts in this project by default, and can expand to everything. Results can include code, messages, findings, and other project context.",
  },
  "project-artifacts": {
    priority: 70,
    preferredPlacement: "bottom",
    title: "Artifacts keep useful outputs together",
    body: "Images and other saved outputs from your chats collect here. Filter them by source or chat, then open an item or jump back to where it came from.",
  },
  "project-security": {
    priority: 80,
    preferredPlacement: "bottom",
    title: "Security scans keep findings in context",
    body: "Each scan is recorded here with its findings. Choose a run, filter by severity, and inspect a finding alongside its project location and guidance.",
  },
  "project-extensions": {
    priority: 90,
    preferredPlacement: "right",
    title: "Extensions shape this project",
    body: "Use project extensions to choose the packs, workflows, skills, and policies available in this workspace without changing your other projects.",
  },
  "selected-chat": {
    priority: 15,
    preferredPlacement: "right",
    title: "One chat is always selected",
    body: "The highlighted chat is where your next message goes, and what Files marks changes against. It stays selected when you open a stage — pick another here to switch without leaving.",
  },
  workflows: {
    priority: 50,
    preferredPlacement: "right",
    title: "Workflows guide a repeatable task",
    body: "Choose a workflow here, then send your next message to start it. You can follow its progress and pause, resume, or review it as it runs.",
  },
} as const satisfies Record<string, FirstTimeTip>;

export type FirstTimeTipId = keyof typeof FIRST_TIME_TIPS;

export const FIRST_TIME_TIP_ROLLOUT = (
  Object.entries(FIRST_TIME_TIPS) as [FirstTimeTipId, FirstTimeTip][]
)
  .sort(([, left], [, right]) => left.priority - right.priority)
  .map(([id]) => id);

export function isFirstTimeTipId(value: unknown): value is FirstTimeTipId {
  return typeof value === "string" && value in FIRST_TIME_TIPS;
}
