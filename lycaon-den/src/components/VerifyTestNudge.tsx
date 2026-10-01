import { Show } from "solid-js";
import type { VerifyTestSuggestion } from "../settings/extensions/verify-test-suggestion.ts";
import { VERIFY_SETTINGS_COPY } from "../settings/extensions/verify-settings-copy.ts";
import { SystemNudge } from "./SystemNudge.tsx";

type Props = {
  suggestion: VerifyTestSuggestion;
  /** Opens the project's Tests settings panel so the user can set their own command. */
  onOpenSettings?: () => void;
};

export function VerifyTestNudge(props: Props) {
  const suggestion = () => props.suggestion;
  const busy = () => suggestion().busy();

  return (
    <Show when={suggestion().suggesting()}>
      <SystemNudge
        testId="verify-test-nudge"
        title="Set a test command for this project?"
        description={
          <>
            Detected <code>{suggestion().detectedCommand()}</code> in{" "}
            {suggestion().detectedSource() || "your project docs"}.{" "}
            {VERIFY_SETTINGS_COPY.nudgeConsequence}
          </>
        }
        primaryAction={{
          label: busy() ? "Setting…" : "Set command",
          onClick: () => void suggestion().accept(),
          disabled: busy(),
        }}
        secondaryAction={{
          label: "Dismiss",
          onClick: () => void suggestion().dismiss(),
          disabled: busy(),
        }}
        tertiaryAction={
          props.onOpenSettings
            ? {
                label: "Choose your own",
                onClick: () => props.onOpenSettings?.(),
                disabled: busy(),
              }
            : undefined
        }
        onDismiss={() => void suggestion().dismiss()}
      />
    </Show>
  );
}
