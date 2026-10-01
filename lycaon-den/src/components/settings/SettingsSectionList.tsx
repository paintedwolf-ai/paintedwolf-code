import { For, createMemo } from "solid-js";
import { NavSelectionDot, navRow } from "../nav/NavSelectionDot.tsx";

type SectionItem = { id: string; label: string };

type Props = {
  items: readonly SectionItem[];
  active: string;
  ariaLabel: string;
  testidPrefix: string;
  onSelect: (id: string) => void;
};

/** Shared section picker — sub-link rows plus a single guide dot that travels to
 *  the active row. Used by the app Settings fold. */
export function SettingsSectionList(props: Props) {
  const activeIndex = createMemo(() =>
    props.items.findIndex((item) => item.id === props.active),
  );

  return (
    <div class="den-settings-sidebar">
      <nav class="den-shell-nav-sub" aria-label={props.ariaLabel}>
        <ul class="den-shell-nav-sub-list den-settings-sidebar-list">
          <NavSelectionDot index={activeIndex()} />
          <For each={props.items}>
            {(item, index) => (
              <li {...navRow(index())}>
                <button
                  type="button"
                  class="den-shell-nav-sub-link"
                  classList={{
                    "den-shell-nav-sub-link-active": props.active === item.id,
                  }}
                  data-testid={`${props.testidPrefix}-${item.id}`}
                  onClick={() => props.onSelect(item.id)}
                >
                  <span class="den-shell-nav-sub-link-label">{item.label}</span>
                </button>
              </li>
            )}
          </For>
        </ul>
      </nav>
    </div>
  );
}
