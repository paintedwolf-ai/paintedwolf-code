import { createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { prepareWalk } from "./walk-prefetch.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { currentTurnFromStore } from "../../components/project/SidebarScopePicker.tsx";
import type { WalkFileTarget } from "./walk-model.ts";
import {
  enterWalk,
  isWalking,
  leaveWalk,
  refreshWalk,
  subscribeWalk,
} from "./walk-store.ts";

type WalkProps = {
  projectId: string;
  client: LycaonClient | null;
  appStore: AppStore;
  initialFile?: WalkFileTarget | null;
};

export function WalkButton(props: WalkProps) {
  createEffect(() => {
    const client = props.client;
    if (client) void refreshWalk(props.projectId, client);
  });
  const [tick, setTick] = createSignal(0);
  onCleanup(
    subscribeWalk((id) => {
      if (id === props.projectId.trim()) setTick((value) => value + 1);
    }),
  );

  const walking = createMemo(() => {
    void tick();
    return isWalking(props.projectId);
  });
  const currentChat = () => currentTurnFromStore(props.appStore);

  const prepare = () => {
    const chat = currentChat();
    if (props.client && chat && !walking()) prepareWalk(props.client, props.projectId, chat.sessionId, props.initialFile);
  };

  const toggle = () => {
    if (walking()) {
      leaveWalk(props.projectId);
      return;
    }
    const chat = currentChat();
    if (!chat) return;
    void enterWalk(props.projectId, props.client, chat.sessionId, props.initialFile);
  };

  return (
    <button
      type="button"
      class="den-quiet-icon-btn"
      data-testid="walk-button"
      data-tip={walking() ? "Close walk (Esc)" : "Walk this chat in time order"}
      data-tip-pos="below"
      aria-label={walking() ? "Close walk" : "Walk this chat in time order"}
      aria-pressed={walking()}
      disabled={!walking() && (!props.client || !currentChat())}
      onPointerEnter={prepare}
      onFocus={prepare}
      onClick={toggle}
    >
      <ThemeIcon slot="walk" size={16} />
    </button>
  );
}
