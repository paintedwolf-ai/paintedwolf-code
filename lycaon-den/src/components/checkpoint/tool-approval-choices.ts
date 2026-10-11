import { createMemo, createSignal, createEffect, on } from "solid-js";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { optionForSlot } from "../../chat/checkpoint/approval-slots.ts";
import { presenceAvailable } from "../../platform/presence.ts";
import { chatUnlocked } from "../../chat/vault/chat-vault-store.ts";
import type { ApprovalCardProps } from "./approval-card-types.ts";

export function useToolApprovalChoices(props: ApprovalCardProps) {
  const copy = APPROVALS_COPY.card;
  const plan = () => props.checkpoint.tool_approval?.plan;
  const [chosenDirectory, setChosenDirectory] = createSignal("");
  createEffect(on(() => plan()?.id, () => setChosenDirectory("")));
  const directoryScopes = createMemo(() => plan()?.directory_scopes ?? []);
  const directoryScope = createMemo(() => directoryScopes().includes(chosenDirectory())
    ? chosenDirectory() : directoryScopes()[0] ?? "");
  const offeredOptions = createMemo(() => plan()?.options ?? []);
  const options = createMemo(() => offeredOptions().filter((option) =>
    !option.directory_scope || option.directory_scope === directoryScope()));
  const recommended = createMemo(() => {
    const id = plan()?.recommended_option_id;
    const original = offeredOptions().find((option) => option.id === id);
    return options().find((option) => option.id === id) ??
      options().find((option) => option.rung === original?.rung && option.group === original?.group &&
        option.kind === original?.kind && !option.disabled);
  });
  const held = () => plan()?.held_release;
  // Approving a held send while the chat is locked needs the desktop shell
  // to confirm the person.
  const heldBlocked = (option?: { decision_action: string }) =>
    !!held() && option?.decision_action === "approve" && !presenceAvailable() &&
    !chatUnlocked(held()?.chat_session_id);
  const heldReaders = createMemo(() => {
    const kinds = new Set((held()?.recipients ?? []).map((recipient) => recipient.kind));
    return [...kinds].map((kind) => copy.held.reader[kind]);
  });
  const recommendedDisabled = () => recommended()?.disabled === true || heldBlocked(recommended());
  const alternatives = createMemo(() =>
    options().filter((option) => option.id !== recommended()?.id),
  );
  const grantMenuNote = createMemo(() => {
    const reaskWhen = recommended()?.reask_when;
    return reaskWhen ? copy.grantMenu.reask(reaskWhen) : undefined;
  });
  const pickSlot = (slot: number) => {
    const option = optionForSlot(options(), slot);
    if (option) selectOption(option.id);
  };
  const faceMeta = createMemo(() => {
    const option = recommended();
    if (!option) return undefined;
    return copy.faceMeta(option.coverage, option.expires_when);
  });
  const selectOption = (id: string) => {
    const option = options().find((candidate) => candidate.id === id);
    if (!option || option.disabled || heldBlocked(option)) return;
    props.onToolApproval("approve", { optionId: option.id });
  };

  return { directoryScopes, directoryScope, setChosenDirectory, recommended, held, heldBlocked, heldReaders, recommendedDisabled, alternatives, grantMenuNote, pickSlot, faceMeta, selectOption };
}
