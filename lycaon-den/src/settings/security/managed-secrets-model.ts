import type { ManagedSecret } from "../../api/types.ts";

export type SecretStateFilter = "live" | "all" | "revoked";
export type SecretOriginFilter = "all" | ManagedSecret["origin"];

export type SecretFilter = {
  state: SecretStateFilter;
  origin: SecretOriginFilter;
  query: string;
};

export const DEFAULT_SECRET_FILTER: SecretFilter = {
  state: "live",
  origin: "all",
  query: "",
};

export const SECRET_NAME_MAX = 80;
export const SECRET_PURPOSE_MAX = 240;
const SECRET_VALUE_MAX_BYTES = 64 * 1024;
const SECRET_VALUE_MIN_RUNES = 4;

/** Agent-use deadlines may be at most one year ahead. */
const MAX_LIFETIME_DAYS = 365;

export function isRevoked(secret: ManagedSecret): boolean {
  return secret.state === "revoked";
}

function matchesQuery(secret: ManagedSecret, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return (
    secret.name.toLowerCase().includes(needle) ||
    (secret.purpose ?? "").toLowerCase().includes(needle)
  );
}

function matchesState(secret: ManagedSecret, state: SecretStateFilter): boolean {
  if (state === "all") return true;
  if (state === "revoked") return isRevoked(secret);
  return !isRevoked(secret);
}

export function filterSecrets(
  items: ManagedSecret[],
  filter: SecretFilter,
): ManagedSecret[] {
  return items.filter(
    (secret) =>
      matchesState(secret, filter.state) &&
      (filter.origin === "all" || secret.origin === filter.origin) &&
      matchesQuery(secret, filter.query),
  );
}

export function hiddenRevokedCount(
  items: ManagedSecret[],
  filter: SecretFilter,
): number {
  if (filter.state !== "live") return 0;
  return items.filter(
    (secret) =>
      isRevoked(secret) &&
      (filter.origin === "all" || secret.origin === filter.origin) &&
      matchesQuery(secret, filter.query),
  ).length;
}

export function sortSecretsByNewest(items: ManagedSecret[]): ManagedSecret[] {
  return [...items].sort(
    (left, right) => Date.parse(right.created_at) - Date.parse(left.created_at),
  );
}

/** Unavailable values use the replacement form. */
export function needsValue(secret: ManagedSecret): boolean {
  return secret.state === "unavailable";
}

/** Only a chat capability can widen, and only to the project. */
/** A jar is kept from service responses; a person revokes one rather than replace it. */
export function canReplaceValue(secret: ManagedSecret): boolean {
  return secret.origin !== "cookie_jar" && secret.origin !== "token_jar";
}

export function canPromote(secret: ManagedSecret): boolean {
  return secret.scope === "chat" && !isRevoked(secret);
}

export type SecretDraft = {
  name: string;
  purpose: string;
  value: string;
  agentUseEndsAt: string;
};

export type DraftProblem =
  | "name_missing"
  | "name_too_long"
  | "purpose_missing"
  | "purpose_too_long"
  | "value_missing"
  | "value_too_short"
  | "value_too_long";

export function labelProblem(
  name: string,
  purpose: string,
  purposeRequired: boolean,
): DraftProblem | undefined {
  const trimmedName = name.trim();
  if (!trimmedName) return "name_missing";
  if (graphemeCount(trimmedName) > SECRET_NAME_MAX) return "name_too_long";
  const trimmedPurpose = purpose.trim();
  if (purposeRequired && !trimmedPurpose) return "purpose_missing";
  if (graphemeCount(trimmedPurpose) > SECRET_PURPOSE_MAX) return "purpose_too_long";
  return undefined;
}

function graphemeCount(value: string): number {
  return Array.from(new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(value)).length;
}

export function valueProblem(value: string): DraftProblem | undefined {
  if (!value) return "value_missing";
  if (Array.from(value).length < SECRET_VALUE_MIN_RUNES) return "value_too_short";
  if (new TextEncoder().encode(value).length > SECRET_VALUE_MAX_BYTES) {
    return "value_too_long";
  }
  return undefined;
}

/** The value is not trimmed: leading and trailing bytes can be part of it. */
export function addDraftProblem(draft: SecretDraft): DraftProblem | undefined {
  return labelProblem(draft.name, draft.purpose, true) ?? valueProblem(draft.value);
}

export type AgentUseDeadlineChoice = "never" | "30d" | "90d" | "1y" | "custom";

export const AGENT_USE_DEADLINE_CHOICES: AgentUseDeadlineChoice[] = [
  "never",
  "30d",
  "90d",
  "1y",
  "custom",
];

const CHOICE_DAYS: Partial<Record<AgentUseDeadlineChoice, number>> = {
  "30d": 30,
  "90d": 90,
  "1y": MAX_LIFETIME_DAYS,
};

/** Custom deadlines use local end-of-day. */
export function resolveAgentUseDeadline(
  choice: AgentUseDeadlineChoice,
  customDate: string,
  now: Date,
): { at: string } | { problem: "date_missing" | "date_past" | "date_too_far" } {
  if (choice === "never") return { at: "" };
  const days = CHOICE_DAYS[choice];
  if (days != null) {
    return { at: new Date(now.getTime() + days * 86_400_000).toISOString() };
  }
  const trimmed = customDate.trim();
  if (!trimmed) return { problem: "date_missing" };
  const parsed = new Date(`${trimmed}T23:59:59`);
  if (Number.isNaN(parsed.valueOf())) return { problem: "date_missing" };
  if (parsed.getTime() <= now.getTime()) return { problem: "date_past" };
  if (parsed.getTime() > now.getTime() + MAX_LIFETIME_DAYS * 86_400_000) {
    return { problem: "date_too_far" };
  }
  return { at: parsed.toISOString() };
}
