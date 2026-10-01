import type { ApprovalOption, ApprovalOptionRung } from "../../api/types.ts";

/** The ladder has four keyboard slots, one per rung, on every card. */
export const APPROVAL_SLOT_COUNT = 4;

// A secret card's Send redacted row follows the four slots and has no digit.
const SLOT_BY_RUNG: Partial<Record<ApprovalOptionRung, number>> = {
  once: 1,
  unchanged: 1,
  tracked: 1,
  day: 2,
  chat: 3,
  project: 4,
  device: 4,
};

/**
 * The keyboard slot an option answers to. Slots follow the host's rung, not
 * menu position, so the digit a row shows is the digit that picks it; grouped
 * options have none.
 */
export function approvalSlot(option: Pick<ApprovalOption, "rung" | "group">): number | undefined {
  if (option.group?.trim()) return undefined;
  return SLOT_BY_RUNG[option.rung];
}

/** The enabled option a slot digit picks from the host's ladder, if any. */
export function optionForSlot(options: readonly ApprovalOption[], slot: number): ApprovalOption | undefined {
  return options.find((option) => approvalSlot(option) === slot && !option.disabled);
}
