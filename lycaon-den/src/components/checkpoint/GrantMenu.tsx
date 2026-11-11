import { createEffect, createSignal, For, Match, Show, Switch } from "solid-js";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { focusFirstWithoutScroll, focusWithoutScroll } from "../../platform/interaction/focus.ts";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { DenButton } from "../primitives/DenButton.tsx";

type GrantMenuItem = {
  id: string;
  title: string;
  /** Host coverage, under the title on every row. */
  meta?: string;
  /** Ladder heading for a second subject; consecutive same groups share one. */
  group?: string;
  /** The digit that picks this row from the card; grouped rows have none. */
  slot?: number;
  /** A rung that stays in its slot but cannot be picked here; note says why. */
  disabled?: boolean;
  note?: string;
};

type GrantMenuRow =
  | { kind: "sep" }
  | { kind: "heading"; group: string }
  | { kind: "item"; item: GrantMenuItem; index: number };

export function GrantMenu(p: {
  face: { id?: string; title: string };
  items: GrantMenuItem[];
  note?: string;
  disabled?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPick: (id: string) => void;
  testid?: string;
}) {
  let menuEl: HTMLDivElement | undefined;
  let buttonEl: HTMLButtonElement | undefined;
  const [active, setActive] = createSignal(0);

  const close = (refocus = true) => {
    p.onOpenChange(false);
    if (refocus) focusWithoutScroll(buttonEl);
  };

  createEffect(() => {
    if (!p.open) return;
    setActive(0);
    queueMicrotask(() => {
      focusFirstWithoutScroll<HTMLButtonElement>(menuEl, "[role=menuitem]");
    });
  });

  const onMenuKeyDown = (event: KeyboardEvent) => {
    event.stopPropagation();
    const count = p.items.length;
    switch (event.key) {
      case "Escape":
        event.preventDefault();
        close();
        break;
      case "ArrowDown":
        event.preventDefault();
        setActive((i) => (i + 1) % count);
        focusItem();
        break;
      case "ArrowUp":
        event.preventDefault();
        setActive((i) => (i - 1 + count) % count);
        focusItem();
        break;
      case "Home":
        event.preventDefault();
        setActive(0);
        focusItem();
        break;
      case "End":
        event.preventDefault();
        setActive(count - 1);
        focusItem();
        break;
    }
  };

  const focusItem = () => {
    queueMicrotask(() => {
      focusWithoutScroll(
        menuEl?.querySelectorAll<HTMLButtonElement>("[role=menuitem]")[active()],
      );
    });
  };

  const menuRows = (): GrantMenuRow[] => {
    const rows: GrantMenuRow[] = [];
    let lastGroup = "";
    p.items.forEach((item, index) => {
      const group = item.group?.trim() ?? "";
      if (group && group !== lastGroup) {
        if (rows.length > 0) rows.push({ kind: "sep" });
        rows.push({ kind: "heading", group });
        lastGroup = group;
      } else if (!group && lastGroup) {
        if (rows.length > 0) rows.push({ kind: "sep" });
        lastGroup = "";
      }
      rows.push({ kind: "item", item, index });
    });
    return rows;
  };

  return (
    <Show when={p.items.length > 0 || p.face.id}>
      <div class="den-approval-grant-menu-host">
        <span class="den-approval-grant-split">
          <Show
            when={p.face.id}
            keyed
            fallback={
              <DenButton
                ref={(el: HTMLButtonElement) => (buttonEl = el)}
                variant="secondary"
                class="den-approval-grant-face"
                data-testid={p.testid ?? "approval-grant-face"}
                disabled={p.disabled}
                aria-haspopup="menu"
                aria-expanded={p.open}
                onClick={() => p.onOpenChange(!p.open)}
              >
                <span>{p.face.title}</span>
                <span class="den-approval-grant-caret" aria-hidden="true">
                  ▾
                </span>
              </DenButton>
            }
          >
            {(faceId) => (
              <>
                <DenButton
                  variant="secondary"
                  class="den-approval-grant-face--split den-approval-grant-face"
                  data-testid={p.testid ?? "approval-grant-face"}
                  disabled={p.disabled}
                  onClick={() => p.onPick(faceId)}
                >
                  {p.face.title}
                </DenButton>
                <Show when={p.items.length > 0}>
                  <DenButton
                    ref={(el: HTMLButtonElement) => (buttonEl = el)}
                    variant="secondary"
                    class="den-approval-grant-more"
                    data-testid="approval-grant-caret"
                    disabled={p.disabled}
                    aria-haspopup="menu"
                    aria-expanded={p.open}
                    aria-label={APPROVALS_COPY.card.grantMenu.moreLabel}
                    onClick={() => p.onOpenChange(!p.open)}
                  >
                    <span class="den-approval-grant-caret" aria-hidden="true">
                      ▾
                    </span>
                  </DenButton>
                </Show>
              </>
            )}
          </Show>
        </span>
        <Show when={p.open && p.items.length > 0}>
          <AnchoredSurface
            ref={(element) => {
              menuEl = element;
            }}
            class="den-approval-grant-menu"
            role="menu"
            ariaLabel={APPROVALS_COPY.card.grantMenu.ariaLabel}
            testId="approval-grant-menu"
            anchor={() => buttonEl}
            preferredSide="top"
            align="end"
            overflow="hidden"
            onDismiss={() => close(false)}
            onKeyDown={onMenuKeyDown}
          >
            <div class="den-approval-grant-list">
              <For each={menuRows()}>
                {(row) => (
                  <Switch>
                    <Match when={row.kind === "sep"}>
                      <div
                        class="den-approval-grant-sep"
                        role="separator"
                        data-testid="approval-grant-sep"
                      />
                    </Match>
                    <Match when={row.kind === "heading" ? row : undefined} keyed>
                      {(heading) => (
                        <div
                          class="den-approval-grant-group"
                          role="presentation"
                          data-testid="approval-grant-group"
                        >
                          {heading.group}
                        </div>
                      )}
                    </Match>
                    <Match when={row.kind === "item" ? row : undefined} keyed>
                      {(itemRow) => (
                        <button
                          type="button"
                          role="menuitem"
                          class="den-approval-grant-item"
                          classList={{
                            "den-approval-grant-item--disabled": Boolean(itemRow.item.disabled),
                          }}
                          data-offer-id={itemRow.item.id}
                          data-slot={itemRow.item.slot}
                          aria-disabled={itemRow.item.disabled ? "true" : undefined}
                          aria-label={
                            itemRow.item.slot
                              ? APPROVALS_COPY.card.grantMenu.slotAria(itemRow.item.slot, itemRow.item.title)
                              : itemRow.item.title
                          }
                          disabled={p.disabled || itemRow.item.disabled}
                          onClick={() => {
                            // Resolution receives the pick before focus leaves the item.
                            p.onPick(itemRow.item.id);
                            close(false);
                          }}
                        >
                          <Show when={itemRow.item.slot} keyed>
                            {(slot) => (
                              <kbd class="den-approval-grant-slot" aria-hidden="true">
                                {APPROVALS_COPY.card.grantMenu.slotKey(slot)}
                              </kbd>
                            )}
                          </Show>
                          <span class="den-approval-grant-item-body">
                            <span class="den-approval-grant-item-title">
                              {itemRow.item.title}
                            </span>
                            <Show
                              when={itemRow.item.disabled ? itemRow.item.note : itemRow.item.meta}
                              keyed
                            >
                              {(meta) => (
                                <span
                                  class="den-approval-grant-item-meta"
                                  data-testid={
                                    itemRow.item.disabled
                                      ? "approval-grant-item-note"
                                      : "approval-grant-item-meta"
                                  }
                                >
                                  {meta}
                                </span>
                              )}
                            </Show>
                          </span>
                        </button>
                      )}
                    </Match>
                  </Switch>
                )}
              </For>
            </div>
            <Show when={p.note}>
              <p class="den-approval-grant-note">{p.note}</p>
            </Show>
          </AnchoredSurface>
        </Show>
      </div>
    </Show>
  );
}

