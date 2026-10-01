import { Show } from "solid-js";
import { proseProps } from "../styling/ui-chrome.ts";
import type { AppNotice } from "./notice-model.ts";
import { DenButton } from "../components/primitives/DenButton.tsx";

/** A recovery action a caller can offer alongside its suggested-action copy. */
export type InlineNoticeAction = {
  label: string;
  onClick: () => void | Promise<void>;
  disabled?: boolean;
  testId?: string;
};

type Props = {
  notice: AppNotice | undefined;
  testId?: string;
  /** Rendered as a button beneath the suggested-action text, when set. */
  action?: InlineNoticeAction;
};

/** Catalog notice for an action that failed in this dialog. */
export function InlineNotice(props: Props) {
  return (
    <Show when={props.notice} keyed>
      {(notice) => (
        <div
          class="den-inline-notice"
          data-testid={props.testId ?? "inline-notice"}
          data-code={notice.code}
          role="alert"
          {...proseProps()}
        >
          <Show when={notice.title !== notice.message}>
            <p class="den-inline-notice__title">{notice.title}</p>
          </Show>
          <p class="den-inline-notice__message">{notice.message}</p>
          <Show when={notice.suggestedAction} keyed>
            {(action) => (
              <p class="den-inline-notice__hint">{action}</p>
            )}
          </Show>
          <Show when={props.action} keyed>
            {(action) => (
              <div class="den-inline-notice__actions">
                <DenButton
                  variant="secondary"
                  compact
                  data-testid={action.testId ?? "inline-notice-action"}
                  disabled={action.disabled}
                  onClick={() => void action.onClick()}
                >
                  {action.label}
                </DenButton>
              </div>
            )}
          </Show>
        </div>
      )}
    </Show>
  );
}
