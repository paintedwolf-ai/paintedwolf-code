import { ChatTopChromeSlot } from "./ChatTopChromeSlot.tsx";
import type { SlotContent } from "../../ui/slot-content.ts";

type Props = {
  /** This chat's own notices. */
  session?: SlotContent;
  /** Session spend against its ceiling — approaching, and parked at it. */
  spend?: SlotContent;
  /** Standing app and project notifications — the same stack as every stage. */
  notifications?: SlotContent;
  /** Project verify-command suggestion. */
  verify?: SlotContent;
  /** Mediation and sandbox protection chrome. */
  protection?: SlotContent;
  /** Draft → saved-project promote nudge. */
  promote?: SlotContent;
  /** No ready provider / default model configured. */
  provider?: SlotContent;
  /** Saved project with no folder attached. */
  folder?: SlotContent;
};

/** Ordered collapsible chrome beneath the chat tab rail. */
export function ChatTopChromeStack(props: Props) {
  return (
    <div class="den-chat-top-dock" data-testid="chat-top-chrome-stack">
      <ChatTopChromeSlot
        slot="session"
        present={props.session?.present() === true}
      >
        {props.session?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot slot="spend" present={props.spend?.present() === true}>
        {props.spend?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot
        slot="notifications"
        present={props.notifications?.present() === true}
      >
        {props.notifications?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot
        slot="verify"
        present={props.verify?.present() === true}
      >
        {props.verify?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot
        slot="protection"
        present={props.protection?.present() === true}
      >
        {props.protection?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot
        slot="promote"
        present={props.promote?.present() === true}
      >
        {props.promote?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot
        slot="provider"
        present={props.provider?.present() === true}
      >
        {props.provider?.children()}
      </ChatTopChromeSlot>
      <ChatTopChromeSlot slot="folder" present={props.folder?.present() === true}>
        {props.folder?.children()}
      </ChatTopChromeSlot>
    </div>
  );
}
