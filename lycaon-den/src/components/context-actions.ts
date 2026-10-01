import { contributionCommand, contributionCommandAvailable, nativeCommandId } from "../contributions/dispatch.ts";
import { bindingForHandler } from "../shortcuts/display-binding-for.ts";
import type { ContextMenuItem } from "./ContextMenu.tsx";
import { CONTEXT_ACTIONS, type ContextActionId } from "./context-action-catalog.ts";

type WithoutPresentation<T> = T extends unknown ? Omit<T, "label" | "danger"> : never;
type ActionItem = Exclude<ContextMenuItem, { separator: true }>;
export type ContextActionHandler = () => unknown;
type WithHandler<T, Handler> = T extends { onSelect: ContextActionHandler }
  ? Omit<T, "onSelect"> & { onSelect: Handler } : T;
type ActionBinding = WithoutPresentation<WithHandler<ActionItem, ContextActionHandler>>;
type ActionPresentation = Pick<ActionItem, "label" | "danger" | "disabled" | "shortcut">;
// The binding consumes async outcomes; menu callbacks always return void.
type BoundAction<T> = WithHandler<T, () => void>;

/** Captured targets use the catalog and current command facts. */
export function contextAction<T extends ActionBinding>(
  id: ContextActionId,
  binding: T,
): BoundAction<T & ActionPresentation> {
  const definition = CONTEXT_ACTIONS[id];
  const handler = "handler" in definition ? definition.handler : undefined;
  const commandId = handler ? nativeCommandId(handler) : null;
  const command = commandId ? contributionCommand(commandId) : null;
  const shortcut = handler ? bindingForHandler(handler).trim() : undefined;
  return bindContextAction<T & ActionPresentation>({
    ...binding,
    label: command?.title ?? definition.label,
    ...("danger" in definition ? { danger: definition.danger } : {}),
    ...(shortcut && shortcut !== "—" ? { shortcut } : {}),
  }, handler ? () => commandId != null && contributionCommandAvailable(commandId) : undefined);
}

/** Dynamic destinations and contributed actions share the same dispatch boundary. */
export function bindContextAction<T extends { label: string; disabled?: boolean; onSelect?: ContextActionHandler }>(
  item: T,
  available?: () => boolean,
): BoundAction<T> {
  const onSelect = item.onSelect;
  return {
    ...item,
    ...(available ? { disabled: item.disabled || !available() } : {}),
    ...(onSelect ? {
      onSelect: () => {
        if (item.disabled || (available && !available())) return;
        try {
          const result: unknown = onSelect();
          void Promise.resolve(result).catch((error) => reportContextActionError(item.label, error));
        } catch (error) {
          void reportContextActionError(item.label, error);
        }
      },
    } : {}),
  } as BoundAction<T>;
}

async function reportContextActionError(label: string, error: unknown): Promise<void> {
  const { appNoticeReporter } = await import("../platform/connection/app-connection.ts");
  appNoticeReporter().publish({
    severity: "error",
    title: `Could not complete “${label}”`,
    message: error instanceof Error ? error.message : String(error),
  });
}
