import { confirmDestructive } from "../platform/interaction/confirm-dialog.ts";

/** Confirm for the one destructive chat verb. */
export function confirmChatDelete(count = 1): Promise<boolean> {
  return confirmDestructive({
    message:
      count === 1
        ? "Delete this chat and its full history? This cannot be undone."
        : `Delete ${count} chats and their full history? This cannot be undone.`,
    title: count === 1 ? "Delete chat" : "Delete chats",
    okLabel: count === 1 ? "Delete" : `Delete ${count}`,
  });
}
