import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AgentWatch, AgentWatchReceipt } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { formatHostWatchError } from "../../watches/watch-format.ts";
import {
  cancelSessionWatchAction,
  inspectSessionWatchAction,
  setSessionWatchDeliveryAction,
} from "../../watches/watches-actions.ts";
import {
  ensureSessionWatchesHydrated,
  sessionWatchesFor,
} from "../../watches/watches-store.ts";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  part: ToolPartView;
  sessionId?: string;
  projectId?: string;
  processId?: string;
  client?: LycaonClient | null;
};

function parseWatchHandlesFromPart(part: ToolPartView): string[] {
  const handles: string[] = [];
  const tool = part.tool.trim().toLowerCase();
  if (tool === "watch_control" && typeof part.args?.handle === "string") {
    const h = part.args.handle.trim();
    if (h) handles.push(h);
  }
  if (typeof part.output === "string" && part.output.trim().startsWith("{")) {
    try {
      const parsed = JSON.parse(part.output) as Record<string, unknown>;
      if (tool === "watch" && typeof parsed.handle === "string") {
        handles.push(parsed.handle.trim());
      }
      if (Array.isArray(parsed.derived_watches)) {
        for (const item of parsed.derived_watches) {
          if (
            item &&
            typeof item === "object" &&
            typeof (item as Record<string, unknown>).handle === "string"
          ) {
            handles.push(
              ((item as Record<string, unknown>).handle as string).trim(),
            );
          }
        }
      }
    } catch {
      // Non-JSON tool output carries no watch handles.
    }
  }
  return [...new Set(handles.filter(Boolean))];
}

function canWakeWatch(watch: AgentWatch): boolean {
  if (watch.owner_kind !== "session" || watch.standing_draft != null) {
    return false;
  }
  if (
    watch.repeat &&
    watch.conditions.some((c) => c.kind === "timer")
  ) {
    return false;
  }
  return true;
}

/** Expanded-only watch controls inside a `watch` or originating `command` ActionCard. */
export function ActionWatchControls(props: Props) {
  const resolveClient = () => props.client ?? getLycaonClient();
  const [busyHandle, setBusyHandle] = createSignal<string | null>(null);
  const [errorByHandle, setErrorByHandle] = createSignal<
    ReadonlyMap<string, string>
  >(new Map());

  const explicitHandles = createMemo(() => parseWatchHandlesFromPart(props.part));
  const isRelevantTool = createMemo(() => {
    const tool = props.part.tool.trim().toLowerCase();
    return (
      tool === "watch" ||
      tool === "watch_control" ||
      tool === "command" ||
      Boolean(props.processId)
    );
  });

  createEffect(() => {
    const sid = props.sessionId?.trim();
    const client = resolveClient();
    if (sid && client && isRelevantTool()) {
      ensureSessionWatchesHydrated(client, sid);
    }
  });

  const matchedWatches = createMemo((): AgentWatch[] => {
    const sid = props.sessionId?.trim();
    if (!sid || !isRelevantTool()) return [];
    const view = sessionWatchesFor(sid);
    const out: AgentWatch[] = [];
    const seen = new Set<string>();
    for (const h of explicitHandles()) {
      const found = view.byHandle.get(h);
      if (found && !seen.has(found.handle)) {
        seen.add(found.handle);
        out.push(found);
      }
    }
    if (props.processId) {
      for (const w of view.watches) {
        if (w.source_process_id === props.processId && !seen.has(w.handle)) {
          seen.add(w.handle);
          out.push(w);
        }
      }
    }
    return out;
  });

  const receiptFor = (handle: string): AgentWatchReceipt | undefined => {
    const sid = props.sessionId?.trim();
    if (!sid) return undefined;
    const view = sessionWatchesFor(sid);
    return (
      view.receiptsByHandle.get(handle) ??
      view.byHandle.get(handle)?.final_receipt
    );
  };

  const setErr = (handle: string, msg: string | null) => {
    setErrorByHandle((prev) => {
      const next = new Map(prev);
      if (msg) next.set(handle, msg);
      else next.delete(handle);
      return next;
    });
  };

  const runAction = async (handle: string, fn: (c: LycaonClient) => Promise<unknown>) => {
    const client = resolveClient();
    if (!client) {
      setErr(handle, "Not connected");
      return;
    }
    setBusyHandle(handle);
    setErr(handle, null);
    try {
      await fn(client);
    } catch (err) {
      setErr(handle, formatHostWatchError(err));
    } finally {
      setBusyHandle(null);
    }
  };

  return (
    <Show when={matchedWatches().length > 0}>
      <div
        class="flex flex-col gap-den-2"
        data-testid="action-watch-controls"
      >
        <For each={matchedWatches()}>
          {(watch) => {
            const sid = () => (props.sessionId ?? "").trim();
            const isBusy = () => busyHandle() === watch.handle;
            const receipt = () => receiptFor(watch.handle);
            const errText = () => errorByHandle().get(watch.handle) ?? null;

            return (
              <div
                class="flex flex-col gap-den-1-5 rounded-den-sm border border-den-line p-den-2"
                data-testid={`action-watch-item-${watch.handle}`}
              >
                <dl class="den-action-card__facts">
                  <div data-kind="watch-handle">
                    <dt>Watch</dt>
                    <dd data-testid={`action-watch-state-${watch.handle}`}>
                      {watch.handle} ·{" "}
                      {watch.state === "live"
                        ? `Live (${watch.delivery})`
                        : `Ended (${watch.terminal_outcome ?? "terminal"})`}
                    </dd>
                  </div>
                  <div data-kind="watch-conditions">
                    <dt>Condition</dt>
                    <dd>{watch.condition_summary}</dd>
                  </div>
                  <Show when={watch.standing_draft} keyed>
                    {(draft) => (
                      <div data-kind="watch-draft">
                        <dt>Standing draft</dt>
                        <dd data-testid={`action-watch-draft-${watch.handle}`}>
                          {draft.name} — {draft.prompt}
                        </dd>
                      </div>
                    )}
                  </Show>
                  <Show when={receipt()} keyed>
                    {(rec) => (
                      <div data-kind="watch-receipt">
                        <dt>Final receipt</dt>
                        <dd data-testid={`action-watch-receipt-${watch.handle}`}>
                          {rec.terminal_outcome ?? rec.state}
                          {rec.standing_watch_id
                            ? ` · standing watch ${rec.standing_watch_id}`
                            : ""}
                        </dd>
                      </div>
                    )}
                  </Show>
                </dl>

                <div class="flex flex-wrap items-center gap-den-2">
                  <Show when={watch.state === "live" && canWakeWatch(watch)}>
                    <DenButton
                      variant="ghost"
                      compact
                      disabled={isBusy()}
                      data-testid={`action-watch-delivery-${watch.handle}`}
                      onClick={() =>
                        void runAction(watch.handle, (c) =>
                          setSessionWatchDeliveryAction(
                            c,
                            sid(),
                            watch.handle,
                            watch.delivery === "wake" ? "collect" : "wake",
                          ),
                        )
                      }
                    >
                      {watch.delivery === "wake"
                        ? "Switch to collect"
                        : "Switch to wake"}
                    </DenButton>
                  </Show>

                  <Show
                    when={
                      watch.state === "live" &&
                      watch.keepable &&
                      !watch.standing_draft &&
                      Boolean(props.projectId?.trim())
                    }
                  >
                    <DenButton
                      variant="secondary"
                      compact
                      disabled={isBusy()}
                      data-testid={`action-watch-keep-${watch.handle}`}
                      onClick={() =>
                        openRunningStage({
                          projectId: props.projectId,
                          prefillFromWatch: {
                            sessionId: sid(),
                            handle: watch.handle,
                          },
                        })
                      }
                    >
                      Keep as standing watch
                    </DenButton>
                  </Show>

                  <Show when={watch.state === "live"}>
                    <DenButton
                      variant="secondary"
                      compact
                      disabled={isBusy()}
                      data-testid={`action-watch-cancel-${watch.handle}`}
                      onClick={() =>
                        void runAction(watch.handle, (c) =>
                          cancelSessionWatchAction(c, sid(), watch.handle),
                        )
                      }
                    >
                      Cancel watch
                    </DenButton>
                  </Show>

                  <DenButton
                    variant="ghost"
                    compact
                    disabled={isBusy()}
                    data-testid={`action-watch-inspect-${watch.handle}`}
                    onClick={() =>
                      void runAction(watch.handle, (c) =>
                        inspectSessionWatchAction(c, sid(), watch.handle),
                      )
                    }
                  >
                    Inspect watch
                  </DenButton>
                </div>

                <Show when={errText()}>
                  {(msg) => (
                    <p
                      class="den-action-card__notice"
                      data-testid={`action-watch-error-${watch.handle}`}
                    >
                      {msg()}
                    </p>
                  )}
                </Show>
              </div>
            );
          }}
        </For>
      </div>
    </Show>
  );
}
