import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { settingAnchor, type SettingId } from "../../settings/settings-registry.ts";

type SettingsListPanelProps = {
  chrome: JSX.Element;
  children: JSX.Element;
  class?: string;
  testId?: string;
  /** The registry setting this list is the place for, such as provider credentials. */
  setting?: SettingId;
};

export function SettingsListPanel(props: SettingsListPanelProps) {
  return (
    <section
      class={cn("den-settings-list-panel", props.class)}
      data-testid={props.testId}
      {...(props.setting ? settingAnchor(props.setting) : {})}
    >
      {props.chrome}
      {props.children}
    </section>
  );
}
