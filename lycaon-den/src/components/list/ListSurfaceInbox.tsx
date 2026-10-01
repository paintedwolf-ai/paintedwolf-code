import { Show, type JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";

type ListSurfaceInboxProps = {
  detailOpen: boolean;
  isEmpty?: boolean;
  empty?: JSX.Element;
  list: JSX.Element;
  detail?: JSX.Element;
  class?: string;
  scrollport?: boolean;
  listTestId?: string;
  detailTestId?: string;
};

export function ListSurfaceInbox(props: ListSurfaceInboxProps) {
  const listBody = () => (
    <Show when={!props.isEmpty} fallback={props.empty}>
      <div data-testid={props.listTestId}>{props.list}</div>
    </Show>
  );
  return (
    <div class={cn("den-settings-list-inbox", props.class)}>
      <Show
        when={props.detailOpen && props.detail}
        fallback={
          <Show
            when={props.scrollport}
            fallback={<div class="den-settings-list-inbox__list">{listBody()}</div>}
          >
            <Scrollport
              class="den-settings-list-inbox__list"
              contentClass="den-settings-list-inbox__list-content"
            >
              {listBody()}
            </Scrollport>
          </Show>
        }
      >
        <Show
          when={props.scrollport}
          fallback={
            <div
              class="den-settings-list-inbox__detail den-settings-list-inbox__detail-content"
              data-testid={props.detailTestId}
            >
              {props.detail}
            </div>
          }
        >
          <Scrollport
            class="den-settings-list-inbox__detail"
            contentClass="den-settings-list-inbox__detail-content"
            data-testid={props.detailTestId}
          >
            {props.detail}
          </Scrollport>
        </Show>
      </Show>
    </div>
  );
}
