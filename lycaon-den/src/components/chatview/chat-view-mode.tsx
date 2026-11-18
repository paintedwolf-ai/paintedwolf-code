import { createEffect, createMemo, createSignal, on, onCleanup, Show, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { SessionMode } from "../../api/types.ts";
import { captureInitialSessionMode, sessionModeView, setDraftSessionMode } from "../../chat/session/session-mode-state.ts";
import { upgradeSessionMode } from "../../chat/session/session-mode-upgrade.ts";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { SessionModeSwitch } from "./SessionModeSwitch.tsx";
import { SESSION_MODE_UPGRADE_REFUSAL } from "./session-mode-copy.ts";

const MODE_EXIT_REST_MS = 200;
const MODE_EXIT_FADE_MS = 180;

type ModeControlProps = {
  sessionId: string;
  active: boolean;
  view: ReturnType<typeof sessionModeView>;
  disconnected: boolean;
  select: (mode: SessionMode) => void;
  resize: (mutate: () => void) => void;
  focusInput: () => void;
};

function ComposerModeControl(props: ModeControlProps) {
  const visible = () => props.active && props.view.show;
  const [mounted, setMounted] = createSignal(visible());
  const displayedMode = createMemo(() => props.view.upgrade ? "agent" as const : props.view.mode);
  const selection = createMemo(() => ({ sessionId: props.sessionId, mode: displayedMode() }));
  const [settled, setSettled] = createSignal<ReturnType<typeof selection>>();
  const confirmedAgent = () => props.view.started && props.view.mode === "agent";
  let element: HTMLDivElement | undefined;

  const remove = () => {
    const restoreFocus = element?.contains(element.ownerDocument.activeElement);
    props.resize(() => setMounted(false));
    if (restoreFocus) props.focusInput();
  };

  createEffect(on(
    [() => props.sessionId, visible, () => props.active, confirmedAgent, () => settled() === selection()],
    ([sessionId, show, active, confirmed, atRest], previous) => {
      if (show) {
        setMounted(true);
        return;
      }
      if (sessionId !== previous?.[0] || !active || !confirmed) {
        setMounted(false);
        return;
      }
      if (!mounted()) return;
      if (!atRest && typeof element?.animate === "function" && !prefersReducedMotion()) return;

      let animation: Animation | undefined;
      const timer = setTimeout(() => {
        const node = element;
        if (!node || typeof node.animate !== "function" || prefersReducedMotion(node.ownerDocument.defaultView)) {
          remove();
          return;
        }
        animation = node.animate({ opacity: [1, 0] }, { duration: MODE_EXIT_FADE_MS, fill: "forwards" });
        animation.onfinish = remove;
        animation.oncancel = remove;
      }, MODE_EXIT_REST_MS);

      onCleanup(() => {
        clearTimeout(timer);
        if (animation) {
          animation.onfinish = null;
          animation.oncancel = null;
          animation.cancel();
        }
      });
    },
  ));

  return (
    <Show when={mounted() && selection().mode}>
      {(mode) => (
        <SessionModeSwitch
          ref={(node) => { element = node; }}
          mode={mode()}
          started={props.view.started}
          upgrade={props.view.upgrade}
          disabled={!visible() || props.view.admissionPending || (props.disconnected && props.view.started)}
          onSelect={props.select}
          onSelectionSettled={(selected) => {
            if (selected === selection().mode) setSettled(selection());
          }}
        />
      )}
    </Show>
  );
}

export function createChatViewMode(
  appStore: AppStore,
  sessionId: Accessor<string>,
  active: Accessor<boolean>,
  connectedClient: Accessor<LycaonClient | null>,
  reportError: () => (error: unknown) => void,
) {
  const view = createMemo(() => sessionModeView(appStore, sessionId()));
  createEffect(() => {
    if (active()) captureInitialSessionMode(appStore, sessionId());
  });

  const select = (mode: SessionMode) => {
    const id = sessionId();
    if (!view().started) {
      setDraftSessionMode(appStore, id, mode);
      return;
    }
    const client = connectedClient();
    if (client && mode === "agent") {
      void upgradeSessionMode(appStore, client, id).catch(() => {
        reportError()(new Error(SESSION_MODE_UPGRADE_REFUSAL));
      });
    }
  };

  return {
    view,
    control: (resize: ModeControlProps["resize"], focusInput: ModeControlProps["focusInput"]) => (
      <ComposerModeControl
        sessionId={sessionId()}
        active={active()}
        view={view()}
        disconnected={!connectedClient()}
        select={select}
        resize={resize}
        focusInput={focusInput}
      />
    ),
  };
}
