import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { cn } from "../../shared/cn.ts";

export type SettingsListBackChromeProps = {
  label: string;
  onBack: () => void;
  testId?: string;
  class?: string;
};

/** Card header when navigated into a row — replaces count|Add. */
export function SettingsListBackChrome(props: SettingsListBackChromeProps) {
  return (
    <div class={cn("den-settings-list-back", props.class)}>
      <button
        type="button"
        class="den-settings-list-back__btn"
        data-testid={props.testId ?? "settings-list-back"}
        onClick={() => props.onBack()}
      >
        <ThemeIcon slot="back" size={14} />
        <span>{props.label}</span>
      </button>
    </div>
  );
}
