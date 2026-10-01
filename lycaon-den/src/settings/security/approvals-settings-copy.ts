import type { ApprovalPosture } from "../../api/types.ts";

/** Per-action consequence copy lives in the registry. */

export interface PostureCard {
  id: ApprovalPosture;
  label: string;
  tagline: string;
}

export const APPROVAL_POSTURE_CARDS: readonly PostureCard[] = [
  {
    id: "light",
    label: "Light",
    tagline:
      "Run freely. Ask only when a credential is about to leave, a tool changed under you, a critical detection matches, or the work runs where nothing can observe it.",
  },
  {
    id: "balanced",
    label: "Balanced",
    tagline:
      "Light, plus high-risk detections, changes to your project's instructions, skills, prompts, and settings, paths outside your attached folders, sensitive locations, and destinations the agent chose rather than the ones your project configured.",
  },
  {
    id: "strict",
    label: "Strict",
    tagline:
      "Balanced, plus each new content or command host, MCP tools, sending content out after this chat has read a credential, and handing a secret the chat generated to its own processes and local services.",
  },
] as const;

export const DEFAULT_APPROVAL_POSTURE: ApprovalPosture = "balanced";

export type ApprovalsSettingsTab = "ask" | "saved" | "detections";

export const DETECTIONS_COPY = {
  tab: "Detections",
  intro:
    "A second layer over the sandbox. Detection packs recognize command patterns that are expensive or hard to undo — cloud deletes, credential changes, publishing — and ask before they run.",
  limits:
    "This layer is deliberately incomplete. It recognizes patterns we have written down; it will miss things it has never seen. The sandbox and approval level are what actually hold — a pack only ever adds a prompt, never removes one. You can allow and stop asking about one noisy rule for a day or for this chat from the approval card; that list lives under Saved approvals → Quieted.",
  levelNote:
    "Critical patterns raise a card or pause a connection at every approval level, including Light — a short list of things that cannot be undone or that change who has access. Balanced adds high, Strict adds medium. Lower levels are ignored.",
  egressNote:
    "One pack watches outbound connections instead of commands, so it catches calls made by scripts rather than by a CLI. Repeated connections to the same destination from one exact action share a decision; another destination or action asks independently, even for a host you already allowed.",
  packsHeading: "Packs",
  rulesLabel: (n: number) => (n === 1 ? "1 rule" : `${n} rules`),
  bundledBadge: "Built in",
  generatedBadge: "From public research",
  extensionBadge: "From an extension",
  deviceBadge: "Yours",
  providerHint: (packID: string) => `Provided by ${packID}`,
  egressBadge: "Holds connections",
  unsupportedBadge: "Inactive",
  unsupportedHint:
    "This rule uses something this version cannot evaluate, so it never runs.",
  emptyState: "No detection packs are installed.",
  loadError: "Could not load detection packs.",
  saveError: "Could not change that pack.",
  pickerError: "Could not open folder picker. Try again.",

  addPack: "Add pack…",
  addPackHint:
    "Choose a folder containing pack.yaml and Sigma rule files. A pack can only add approval prompts, never remove one. Quieting a rule from a card is separate — see Saved approvals → Quieted.",
  addTitle: "Add detection pack",
  addFound: (label: string, n: number) =>
    `${label} — ${n === 1 ? "1 rule" : `${n} rules`}`,
  addInactiveHeading: "Will not run",
  addRehearsalHeading: "Did not do what it says",
  addRehearsalHint:
    "This pack's own fixtures.yaml declares what each rule catches and what it must leave alone. These cases came out the other way when we ran them. The pack still installs — a rule that misses only means no prompt.",
  addIgnoredHeading: "Not copied",
  addIgnoredHint:
    "Only pack.yaml, rules and fixtures are copied. Everything else here is left behind.",
  addConfirm: "Add pack",
  addReplace: "Replace existing pack",
  addReplaceHint:
    "A pack with this name is already installed. Adding will replace its rules.",
  addCancel: "Cancel",
  addError: "Could not read that folder.",

  alsoCovers: (names: readonly string[]) => `Also covers ${names.join(", ")}`,

  recentAsksBadge: (n: number) =>
    n === 1 ? "1 ask since the app started" : `${n} asks since the app started`,

  remove: "Remove",
  removeConfirm: (label: string) => `Remove ${label}? Its rules stop running.`,
  removeBundledHint: "Built-in packs can be turned off, but not removed.",
  removeExtensionHint: (packID: string) =>
    `${packID} provides this pack. Turn it off here, or uninstall that extension under Extensions.`,
  removeError: "Could not remove that pack.",

  loadWarningsHeading: "Not running",
  loadWarningsHint:
    "These rules are part of the pack but are not matching anything right now.",

  editHint:
    "Packs you add are copied into your configuration folder. Edit them there and reopen this tab to pick up changes. Packs from an extension are updated by updating that extension.",

  rejectedHeading: "Ignored in this project",
  rejectedHint:
    "This project's detection-packs.yaml asked for something we could not apply. These lines changed nothing — the packs above are what is actually running here.",
  rejectedRowLabel: (id: string) => (id ? id : "A row with no id"),
  rejectedReason: (code: string): string => {
    switch (code) {
      case "project_unknown_id":
        return "No pack with this id is installed on this device.";
      case "project_fields_forbidden":
        return "A project may only turn a pack on, so a row may set id and nothing else. Turning a pack off is a device decision.";
      case "project_unreadable":
        return "The file could not be read, so none of it was applied.";
      case "duplicate_id":
        return "This id appears more than once.";
      case "invalid_entry":
        return "This row is not a valid pack entry.";
      default:
        return "This row was not applied.";
    }
  },
} as const;

export const APPROVALS_SETTINGS_COPY = {
  title: "Approvals",
  intro:
    "Control when approval cards appear, and review bounded approval leases you chose on earlier cards.",
  tabAsk: "When we ask",
  tabSaved: "Saved approvals",
  tabDetections: DETECTIONS_COPY.tab,
  tabsAriaLabel: "Approvals sections",
  askIntro:
    "One global preference for how often approval cards appear. Active project and device leases live under Saved approvals.",
  approvalLevelHeading: "Approval level",
  offNotice: (savedLevel: string) =>
    `Approvals are off in advanced settings. ${savedLevel} remains saved and will apply when approvals are turned back on.`,
  projectOffNotice:
    "Approvals are off in device Settings. This project can restore its saved approval level without changing the device setting.",
  projectRestore: "Turn approvals on for this project",
  optionsHeading: "Options",
  baselineHeading: "Always on",
  baselineLines: [
    "On macOS, commands start in an OS sandbox that keeps their writes to attached folders, temp, and cache.",
    "Reaching past that box takes a request. Your approval level decides when it asks; direct network, local services, and running outside the sandbox ask at every level before first use.",
    "Allowing a folder from a card grants access to it — it does not add it to your attached folders, and it ends on its own.",
  ],
  aiRationaleLabel: "Show an AI note on approval cards",
  aiRationaleHint:
    "Short descriptive summary of how the action fits your goal. Never changes what auto-approves.",
  managedRulesHeading: "Extension policies",
  managedRulesIntro:
    "Read-only approval rules supplied by enabled device extensions. Change them through Extensions or edit their source pack.",
  managedRulesProjectIntro:
    "Read-only device and repository extension policies. Repository rules can add an ask or deny, but can never remove or replace a device rule.",
  managedRulesEmpty: "No extension policies apply here.",
  managedRuleEffect: (effect: string) => effect === "deny" ? "Block" : "Ask",
  managedRuleScope: (scope: string) => scope === "project" ? "Repository" : "Device",
  saveError: "Could not save your approval setting.",
} as const;

// Device Advanced Settings.
export const NEVER_ASK_COPY = {
  heading: "Turn off approvals entirely",
  label: "Never ask me for approval",
  hint:
    "Light already limits prompts to actions that could take away your ability to recover — deleting a whole cloud project, switching off an audit log, a credential heading out over the network. This removes those too.",
  dangerTitle: "This removes your last chance to say no",
  dangerBody: [
    "With device approvals off, approval checks stop pausing. A mistaken or manipulated instruction runs as you, with your access, and the first you hear of it is the result.",
    "Commands still start in the sandbox, but supported requests for broader write, local-service, direct-network, or outside-the-sandbox access are granted automatically without a card. Hard policy blocks remain enforced.",
    "The sandbox knows nothing about your accounts, so a cloud delete or a publish goes through without a word.",
    "If an outbound request contains a detected secret, it is sent unchanged. You will not get the usual choice to stop it or send a redacted version.",
    "A project may restore approvals for itself. Repository settings can make a project stricter, but they can never turn approvals off.",
  ],
  acknowledgeLabel: "I understand the agent will act without asking me",
  confirm: "Turn off approvals",
  cancel: "Keep approvals on",
  enabledNotice:
    "Approvals are off for this device. Approval asks, including supported boundary widening, are allowed without a card; a project may still restore approvals for itself.",
  turnBackOn: "Turn approvals back on",
  projectBlocked:
    "This is a device setting. A project cannot turn approvals off, but it may restore approvals for itself.",
  saveError: "Could not change the approval setting.",
} as const;

export const DEFAULT_NEVER_ASK = false;

export const DEFAULT_AI_RATIONALE_ENABLED = true;

export function postureLabel(posture: string | undefined): string {
  const card = APPROVAL_POSTURE_CARDS.find((c) => c.id === posture);
  return card?.label ?? "Balanced";
}

/** Compact summary for the project override “following Settings” line. */
export function approvalsFollowingSummary(opts: {
  posture?: string;
  aiRationaleEnabled?: boolean;
  neverAsk?: boolean;
}): string {
  const posture = postureLabel(opts.posture);
  if (opts.neverAsk) return `Off · ${posture} saved`;
  const ai =
    (opts.aiRationaleEnabled ?? DEFAULT_AI_RATIONALE_ENABLED) ? "AI note on" : "AI note off";
  return `${posture} · ${ai}`;
}
