import { createSignal } from "solid-js";
import {
  DEFAULT_CHAT_LIST_SORT,
  type ChatListSort,
} from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const [sort, setSort] = createSignal<ChatListSort>(DEFAULT_CHAT_LIST_SORT);

/** How the sidebar orders unpinned chats, in every project. */
export function chatListSortPref(): ChatListSort {
  return sort();
}

export function syncChatListPrefsFromSnapshot(): void {
  setSort(getAppStateSnapshot().chatList?.sort ?? DEFAULT_CHAT_LIST_SORT);
}

export async function saveChatListSort(next: ChatListSort): Promise<void> {
  setSort(next);
  await persistAppStateInBackground({ chatList: { sort: next } });
}
