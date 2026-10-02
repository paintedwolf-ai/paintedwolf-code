import type { ManagedSecret, ManagedSecretAttestation, ManagedSecretUse } from "../../api/types.ts";
import type {
  AgentUseDeadlineChoice,
  DraftProblem,
  SecretOriginFilter,
  SecretStateFilter,
} from "./managed-secrets-model.ts";

export const MANAGED_SECRETS_COPY = {
  title: "Secrets",
  lede:
    "Generated, entered, and detected credentials live behind references the agent can use without reading their values. You can authenticate to reveal a current value when you need to recover it.",
  listAriaLabel: "Managed secrets",
  countLabel: (total: number) => `${total} secret${total === 1 ? "" : "s"}`,
  hiddenRevoked: (total: number) =>
    `${total} revoked hidden`,
  loadingCount: "Loading secrets…",
  loading: "Loading…",
  refresh: "Refresh",
  refreshing: "Checking…",
  back: "All secrets",
  empty: "No managed secrets in this project.",
  emptyFiltered: "No secrets match this filter.",
  groupProject: "Project scope",
  groupChat: "Chat scope",
  loadError: "Secrets could not be loaded.",
  revokeError: "This secret could not be revoked.",
  invalidReference: "This secret reference is invalid.",

  filterState: "State",
  filterOrigin: "Created from",
  filterQuery: "Search",
  filterQueryPlaceholder: "Name or purpose",

  referenceLabel: "Reference",
  referenceHint:
    "Paste this token into a supported outbound tool argument. It carries no value on its own.",
  copyReference: "Copy",
  copiedReference: "Copied",

  factScope: "Scope",
  factOrigin: "Created from",
  factChat: "Chat",
  factFormat: "Format",
  factEntropy: "Entropy",
  factCreated: "Created",
  factAgentUseEnds: "Agent use ends",
  factState: "State",
  factVersion: "Value version",
  factValueReplaced: "Value replaced",
  noAgentUseDeadline: "No agent-use deadline",
  originalValue: "Original stored value",
  versionLabel: (version: number) =>
    version === 0 ? "None stored" : `Version ${version}`,
  chatScopeHint: "Only the chat that created it, and that chat's workers, can use it.",
  projectScopeHint: "Every chat in this project can use it.",
  chatUntitled: "Untitled chat",
  chatDeleted: "Deleted chat",
  chatDeletedHint:
    "No agent can use this secret now, but it stays active. Reveal, replace, promote, or revoke it here.",

  add: "Add secret",
  addHeading: "Add a secret",
  addLede:
    "Stored behind a reference the agent can use without reading it. Ordinary reads stay value-free; revealing later requires operating-system authentication.",
  addName: "Name",
  addNamePlaceholder: "Stripe test key",
  addPurpose: "Purpose",
  addPurposeHint:
    "Required. Every chat in this project can use this secret, so this is how a later chat tells whether it is the one it needs.",
  addPurposePlaceholder: "Charges the test-mode checkout",
  addValue: "Value",
  addValueHint:
    "Written to the encrypted credential vault. Showing it later requires operating-system authentication.",
  addValuePlaceholder: "Paste the credential",
  addAgentUseDeadline: "Agent-use deadline",
  submitAdd: "Add secret",
  submittingAdd: "Adding…",
  addError: "This secret could not be added.",
  addedExisting: (name: string) =>
    `This value is already managed as “${name}”. Opened it instead of adding a second reference to the same credential.`,
  cancel: "Cancel",
  showValue: "Show",
  hideValue: "Hide",

  edit: "Edit details",
  editHeading: "Edit details",
  editError: "These details could not be saved.",
  save: "Save",
  saving: "Saving…",

  agentUseDeadline: "Set agent-use deadline",
  agentUseDeadlineHeading: "Set agent-use deadline",
  agentUseDeadlineHint:
    "After this deadline the agent cannot substitute the protected value. You can still authenticate to reveal it for recovery. The credential itself and any project file are unchanged.",
  agentUseDeadlineError: "The agent-use deadline could not be set.",
  deadlineChoiceNever: "No deadline",
  deadlineChoice30d: "In 30 days",
  deadlineChoice90d: "In 90 days",
  deadlineChoice1y: "In a year",
  deadlineChoiceCustom: "On a date",
  deadlineCustomLabel: "Date",

  replace: "Replace stored value",
  replaceHeading: "Replace the stored value",
  replaceHint:
    "This changes only Painted Wolf's protected copy. It does not update a project file or rotate, revoke, or activate the credential at its provider. Supported agent tooling can keep using the same reference. The retired value remains available only to secret screening and never resolves again.",
  replaceMarkedHint:
    "This capability was created from a value marked in a project file. Replacing the protected copy does not update that file; change the file and the external credential separately if that is your intent. The reference remains the same.",
  restore: "Restore value",
  restoreHeading: "Restore this value",
  restoreHint:
    "This secret has metadata but no readable value. Supplying it again brings the same reference back to life.",
  newValue: "New value",
  submitReplace: "Replace stored value",
  submitRestore: "Restore value",
  replacing: "Storing…",
  replaceError: "The value could not be replaced.",

  promote: "Make available to the whole project",
  promoteHint:
    "Only the chat that created this secret can use it. Promoting it lets every chat in this project use it, and needs a purpose so those chats can tell what it is.",
  promoting: "Promoting…",
  promoteError: "This secret could not be promoted.",

  usesHeading: "Recent uses",
  usesHint:
    "Resolution and handoff to a program or transport, refusals included. Handoff does not prove delivery or successful authentication. A bounded recent window.",
  usesEmpty: "This reference has never reached a tool call.",
  usesLoading: "Loading uses…",
  usesError: "Recent uses could not be loaded.",
  useCountLabel: (total: number) =>
    total === 0 ? "Never used" : `${total} use${total === 1 ? "" : "s"}`,
  factLastUsed: "Last used",
  neverUsed: "Never",

  revealHeading: "Current value",
  revealHint:
    "The installed app asks the operating system to verify you before showing this value. The agent and ordinary API reads still cannot disclose it.",
  reveal: "Reveal value",
  revealing: "Waiting for authentication…",
  revealError: "This secret could not be revealed.",
  revealUnavailable: "There is no readable current value to reveal.",
  revealedHint: (seconds: number) =>
    `Hides in ${seconds} second${seconds === 1 ? "" : "s"}. It also hides when this window loses focus. Anything you copy stays on the clipboard until you replace it.`,
  copyValue: "Copy value",
  copiedValue: "Value copied",
  copyValueFailed: "Could not copy",
  hideRevealedValue: "Hide now",
  factLastRevealed: "Last revealed",
  neverRevealed: "Never",
  revealCountLabel: (total: number) =>
    total === 0 ? "Never revealed" : `${total} reveal${total === 1 ? "" : "s"}`,
  factCustody: "Who holds it",
  factLastReleased: "Last released",
  releaseCountLabel: (total: number) =>
    total === 0 ? "Never released" : `${total} release${total === 1 ? "" : "s"}`,
  useRecipients: (labels: string) => `To ${labels}`,
  useConfirmed: "Released with your confirmation",

  confirmationsHeading: "Confirmations",
  confirmationsHint:
    "Each time you confirmed with Touch ID, Windows Hello, or your device password, to see this value or to hand it to a recipient.",
  confirmationsEmpty: "You have not confirmed any use of this value.",
  confirmationsError: "Confirmations could not be loaded.",

  revoke: "Revoke",
  revoking: "Revoking…",
  revokeHeading: "Revoke this secret",
  revokeHint:
    "Revoking permanently stops the reference from resolving and ends authenticated reveal for this secret — read the value first if you still need it. Protected screening evidence remains until the project is deleted.",
  revokeConfirmTitle: "Revoke secret?",
  revokeConfirmMessage: (name: string) =>
    `Revoke “${name}”? The reference will permanently stop working and the value can no longer be revealed.`,
  revokeConfirmOk: "Revoke secret",
  revokedTerminal:
    "This secret is revoked. Revoking is permanent, so nothing here can be changed and the value can no longer be revealed.",
} as const;

const SCOPE_LABEL: Record<ManagedSecret["scope"], string> = {
  project: "Project",
  chat: "Chat",
};

const STATE_LABEL: Record<ManagedSecret["state"], string> = {
  active: "Active",
  revoked: "Revoked",
  agent_use_expired: "Agent use expired",
  unavailable: "Unavailable",
};

const ORIGIN_LABEL: Record<ManagedSecret["origin"], string> = {
  generated: "Generated",
  ask_user_response: "Ask user response",
  detected: "Detected and tracked",
  file_marked: "Marked in file",
  composer_marked: "Marked in composer",
  settings_entered: "Entered in settings",
  cookie_jar: "HTTP cookie jar",
  token_jar: "HTTP token jar",
};

const CUSTODY_LABEL: Record<NonNullable<ManagedSecret["custody"]>, string> = {
  person: "You gave it to Painted Wolf Code. Each use asks you to confirm.",
  file: "A project file holds it. Anything that can read that file can read it.",
  chat: "Generated for one chat. Its programs may use it without asking at Light and Balanced.",
  host: "Generated or captured for the agent's work.",
};

const RELEASE_SCOPE_LABEL: Record<NonNullable<ManagedSecretAttestation["release_scope"]>, string> = {
  once: "once",
  chat: "for the chat",
  project: "for the project",
};

const STATE_FILTER_LABEL: Record<SecretStateFilter, string> = {
  live: "In use",
  all: "All",
  revoked: "Revoked",
};

const USE_OUTCOME_LABEL: Record<ManagedSecretUse["outcome"], string> = {
  resolved: "Substituted",
  revoked: "Refused — revoked",
  agent_use_expired: "Refused — agent use expired",
  unavailable: "Refused — no value",
  out_of_scope: "Refused — out of scope",
};

const DRAFT_PROBLEM_MESSAGE: Record<DraftProblem, string> = {
  name_missing: "Give this secret a name.",
  name_too_long: "That name is longer than 80 characters.",
  purpose_missing: "Say what this secret is for.",
  purpose_too_long: "That purpose is longer than 240 characters.",
  value_missing: "Paste the credential to store.",
  value_too_short: "Use at least 4 characters so the credential can be protected without matching ordinary text.",
  value_too_long: "That value is larger than a credential can be.",
};

export function secretScopeLabel(secret: ManagedSecret): string {
  return SCOPE_LABEL[secret.scope];
}

export function secretScopeHint(secret: ManagedSecret): string {
  return secret.scope === "chat"
    ? MANAGED_SECRETS_COPY.chatScopeHint
    : MANAGED_SECRETS_COPY.projectScopeHint;
}

export function secretOriginLabel(secret: ManagedSecret): string {
  return ORIGIN_LABEL[secret.origin];
}

export function originFilterLabel(origin: SecretOriginFilter): string {
  return origin === "all" ? "All" : ORIGIN_LABEL[origin];
}

export function stateFilterLabel(state: SecretStateFilter): string {
  return STATE_FILTER_LABEL[state];
}

export function secretStateLabel(secret: ManagedSecret): string {
  return STATE_LABEL[secret.state];
}

const DELIVERY_LABEL: Record<ManagedSecretUse["delivery"], string> = {
  pending: "Resolved — handoff unconfirmed",
  not_dispatched: "Resolved — not handed off",
  withheld: "Resolved — delivery withheld",
  handed_off: "Handed to executor or transport",
  redacted: "Redacted before handoff",
};

export function secretCustodyLabel(secret: ManagedSecret): string {
  return secret.custody ? CUSTODY_LABEL[secret.custody] : "—";
}

export function secretLastReleasedLabel(secret: ManagedSecret): string {
  return formatSecretTimestamp(secret.last_released_at) ?? MANAGED_SECRETS_COPY.neverRevealed;
}

export function attestationLabel(attestation: ManagedSecretAttestation): string {
  if (attestation.purpose === "reveal") return "Revealed to you";
  const to = attestation.recipients.map((recipient) => recipient.label).join(", ");
  const scope = attestation.release_scope ? RELEASE_SCOPE_LABEL[attestation.release_scope] : "";
  return `Released to ${to || "its recipients"} ${scope}`.trim();
}

export function useOutcomeLabel(use: ManagedSecretUse): string {
  return use.outcome === "resolved" ? DELIVERY_LABEL[use.delivery] : USE_OUTCOME_LABEL[use.outcome];
}

export function draftProblemMessage(problem: DraftProblem): string {
  return DRAFT_PROBLEM_MESSAGE[problem];
}

export function agentUseDeadlineProblemMessage(
  problem: "date_missing" | "date_past" | "date_too_far",
): string {
  switch (problem) {
    case "date_missing":
      return "Pick a date.";
    case "date_past":
      return "Pick a date in the future.";
    default:
      return "Pick a date within a year.";
  }
}

export function agentUseDeadlineChoiceLabel(choice: AgentUseDeadlineChoice): string {
  switch (choice) {
    case "never":
      return MANAGED_SECRETS_COPY.deadlineChoiceNever;
    case "30d":
      return MANAGED_SECRETS_COPY.deadlineChoice30d;
    case "90d":
      return MANAGED_SECRETS_COPY.deadlineChoice90d;
    case "1y":
      return MANAGED_SECRETS_COPY.deadlineChoice1y;
    case "custom":
      return MANAGED_SECRETS_COPY.deadlineChoiceCustom;
  }
}

export function formatSecretTimestamp(
  value: string | null | undefined,
): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

export function secretRowSummary(secret: ManagedSecret): string {
  const purpose = secret.purpose?.trim();
  if (purpose) return purpose;
  return `${ORIGIN_LABEL[secret.origin]} · ${SCOPE_LABEL[secret.scope]}`;
}

export function secretAgentUseEndsLabel(secret: ManagedSecret): string {
  const deadline = formatSecretTimestamp(secret.agent_use_ends_at);
  return deadline ?? MANAGED_SECRETS_COPY.noAgentUseDeadline;
}

export function secretValueReplacedLabel(secret: ManagedSecret): string {
  return (
    formatSecretTimestamp(secret.value_replaced_at) ??
    MANAGED_SECRETS_COPY.originalValue
  );
}

export function secretLastUsedLabel(secret: ManagedSecret): string {
  return (
    formatSecretTimestamp(secret.last_used_at) ?? MANAGED_SECRETS_COPY.neverUsed
  );
}

export function secretLastRevealedLabel(secret: ManagedSecret): string {
  return (
    formatSecretTimestamp(secret.last_revealed_at) ??
    MANAGED_SECRETS_COPY.neverRevealed
  );
}
