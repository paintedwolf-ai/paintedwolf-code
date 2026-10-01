import { Show, type JSX } from "solid-js";
import { EditorCommandBar } from "../../find/EditorCommandBar.tsx";
import { editorCommandBarPlacement } from "../../find/find-controller.ts";
import { ComposerChromeSlot } from "./ComposerChromeSlot.tsx";
import type { SlotContent } from "../../ui/slot-content.ts";

type Props = {
  /** Prevents outgoing stages from mounting incoming session chrome. */
  active: boolean;
  dockRef?: (el: HTMLDivElement) => void;
  /** Absolute overlay outside slot layout. */
  queue: JSX.Element;
  launcher: SlotContent;
  arm: SlotContent;
  checkpoint: SlotContent;
  ask: SlotContent;
  composer: JSX.Element;
};

export function ComposerChromeStack(props: Props) {
  return (
    <div
      class="den-chat-composer-dock"
      data-testid="composer-chrome-stack"
      ref={(el) => props.dockRef?.(el)}
    >
      {props.queue}
      <Show when={props.active}>
        {/* The mounted launcher only animates its exit. */}
        <ComposerChromeSlot
          slot="launcher"
          motion="fade"
          present={props.launcher.present()}
        >
          {props.launcher.children()}
        </ComposerChromeSlot>
        <ComposerChromeSlot slot="arm" present={props.arm.present()}>
          {props.arm.children()}
        </ComposerChromeSlot>
        <ComposerChromeSlot
          slot="find"
          present={editorCommandBarPlacement() === "composer"}
        >
          <div
            class="den-find-bar-host--composer"
            data-testid="find-bar-host-composer"
          >
            <EditorCommandBar />
          </div>
        </ComposerChromeSlot>
        <ComposerChromeSlot slot="checkpoint" present={props.checkpoint.present()}>
          {props.checkpoint.children()}
        </ComposerChromeSlot>
        {/* The ask stays closest because Send routes to it first. */}
        <ComposerChromeSlot slot="ask_user" present={props.ask.present()}>
          {props.ask.children()}
        </ComposerChromeSlot>
      </Show>
      {props.composer}
    </div>
  );
}
