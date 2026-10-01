import { For, Show } from "solid-js";
import type { JSX } from "solid-js";
import type { HomeSection } from "../../home/home-model.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";

type Props = {
  section: HomeSection;
  searchActive: boolean;
  onSectionChange: (section: HomeSection) => void;
  onOpenSearch: () => void;
  draftCount: number;
};

const ITEMS: { id: HomeSection; label: string; icon: () => JSX.Element }[] = [
  { id: "recents", label: "Recents", icon: () => <IconClock /> },
  { id: "drafts", label: "Drafts", icon: () => <IconDraft /> },
  { id: "starred", label: "Starred", icon: () => <IconStar /> },
  { id: "all", label: "All projects", icon: () => <IconList /> },
];

export function HomeNav(props: Props) {
  return (
    <nav class="home-nav" aria-label="Main" data-testid="home-nav">
      <button
        type="button"
        class="home-nav__item home-nav__search"
        classList={{ "home-nav__item--active": props.searchActive }}
        data-testid="home-nav-search"
        aria-current={props.searchActive ? "page" : undefined}
        onClick={() => props.onOpenSearch()}
      >
        <span class="home-nav__item-icon" aria-hidden="true">
          <ThemeIcon slot="search" size={16} />
        </span>
        <span class="home-nav__item-label">Search</span>
      </button>

      <ul class="home-nav__list">
        <For each={ITEMS}>
          {(item) => (
            <li>
              <button
                type="button"
                class="home-nav__item"
                classList={{
                  "home-nav__item--active":
                    !props.searchActive && props.section === item.id,
                }}
                data-testid={`home-nav-${item.id}`}
                aria-current={
                  !props.searchActive && props.section === item.id
                    ? "page"
                    : undefined
                }
                onClick={() => props.onSectionChange(item.id)}
              >
                <span class="home-nav__item-icon" aria-hidden="true">{item.icon()}</span>
                <span class="home-nav__item-label">{item.label}</span>
                <Show when={item.id === "drafts" && props.draftCount > 0}>
                  <span class="home-nav__count">{props.draftCount}</span>
                </Show>
              </button>
            </li>
          )}
        </For>
      </ul>
    </nav>
  );
}

function IconClock() {
  return (
    <ThemeIcon slot="recents" size={16} />
  );
}
function IconDraft() {
  return (
    <ThemeIcon slot="drafts" size={16} />
  );
}
function IconStar() {
  return (
    <ThemeIcon slot="starred" size={16} />
  );
}
function IconList() {
  return (
    <ThemeIcon slot="all-projects" size={16} />
  );
}
