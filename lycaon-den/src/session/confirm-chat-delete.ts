import type { LycaonClient } from "../api/client.ts";
import type { ProjectBackgroundProcess } from "../api/types.ts";
import { sessionTitle } from "../chat/session/session-title.ts";
import {
  confirmDestructive,
  type ConfirmDestructivePresenter,
  type ConfirmDestructiveRequest,
} from "../platform/interaction/confirm-dialog.ts";

export type ChatDeleteTarget = {
  sessionId: string;
  title?: string;
};

export type RunningCommandSummary = {
  process_id?: string;
  handle?: string;
  session_id?: string;
  session_title?: string;
  session_archived?: boolean;
  command?: string;
  ports?: readonly string[];
  root_id?: string;
};

export type ChatDeleteClient = Partial<
  Pick<
    LycaonClient,
    "listProjectBackgroundProcesses" | "listSessionBackgroundProcesses"
  >
>;

export type ChatDeleteConfirmOptions = {
  count?: number;
  chats?: readonly ChatDeleteTarget[];
  projectId?: string;
  client?: ChatDeleteClient | null;
  processes?: readonly RunningCommandSummary[];
  onOpenRunning?: () => void;
  confirm?: ConfirmDestructivePresenter;
};

/** Formats a command line and its listening ports for destructive confirmation dialogs. */
export function formatBackgroundCommandLine(
  command: string | undefined,
  ports?: readonly string[],
): string {
  const cmd = command?.trim() || "Background command";
  const cleanPorts = (ports ?? []).map((p) => p.trim()).filter(Boolean);
  return cleanPorts.length > 0 ? `${cmd} (${cleanPorts.join(", ")})` : cmd;
}

/** Formats a command line, ports, and owning chat title for project/root dialogs. */
export function formatBackgroundCommandWithChat(
  proc: RunningCommandSummary,
): string {
  const line = formatBackgroundCommandLine(proc.command, proc.ports);
  const title = sessionTitle(proc.session_title);
  const chatLabel = proc.session_archived ? `${title} (archived)` : title;
  return proc.session_id || proc.session_title ? `${line} — ${chatLabel}` : line;
}

export function runningCommandSignature(
  processes: readonly RunningCommandSummary[],
): string {
  return processes
    .map((p) => {
      const id = (p.process_id ?? p.handle ?? "").trim();
      const sid = (p.session_id ?? "").trim();
      const cmd = (p.command ?? "").trim();
      const ports = [...(p.ports ?? [])].map((port) => port.trim()).sort().join(",");
      return `${sid}:${id}:${cmd}:${ports}`;
    })
    .sort()
    .join("|");
}

export function sameRunningCommandList(
  a: readonly RunningCommandSummary[],
  b: readonly RunningCommandSummary[],
): boolean {
  return runningCommandSignature(a) === runningCommandSignature(b);
}

export async function fetchChatRunningCommands(
  client: ChatDeleteClient | null | undefined,
  projectId: string | undefined,
  chats: readonly ChatDeleteTarget[],
): Promise<RunningCommandSummary[]> {
  if (!client || chats.length === 0) return [];
  const targetIds = new Set(chats.map((c) => c.sessionId.trim()).filter(Boolean));
  if (targetIds.size === 0) return [];
  const titleBySession = new Map<string, string>();
  for (const chat of chats) {
    if (chat.sessionId && chat.title) {
      titleBySession.set(chat.sessionId.trim(), chat.title);
    }
  }
  const pid = projectId?.trim();
  if (pid && typeof client.listProjectBackgroundProcesses === "function") {
    try {
      const resp = await client.listProjectBackgroundProcesses(pid);
      return (resp.processes ?? [])
        .filter((proc) => proc.running && targetIds.has(proc.session_id))
        .map((proc) => ({
          ...proc,
          session_title:
            proc.session_title || titleBySession.get(proc.session_id),
        }));
    } catch {
      return [];
    }
  }
  if (typeof client.listSessionBackgroundProcesses === "function") {
    const collected: RunningCommandSummary[] = [];
    for (const chat of chats) {
      const sid = chat.sessionId.trim();
      if (!sid) continue;
      try {
        const list = await client.listSessionBackgroundProcesses(sid);
        for (const proc of list ?? []) {
          if (!proc.running) continue;
          collected.push({
            process_id: proc.process_id,
            session_id: sid,
            session_title: chat.title,
            command: (proc as { command?: string }).command,
          });
        }
      } catch {
        // Ignore unavailable per-session query.
      }
    }
    return collected;
  }
  return [];
}

export function buildChatDeleteConfirmRequest(options: {
  count: number;
  chats?: readonly ChatDeleteTarget[];
  processes: readonly RunningCommandSummary[];
  onOpenRunning?: () => void;
}): ConfirmDestructiveRequest {
  const count = Math.max(1, options.count);
  const processes = options.processes;
  if (processes.length === 0) {
    return {
      message:
        count === 1
          ? "Delete this chat and its full history? This cannot be undone."
          : `Delete ${count} chats and their full history? This cannot be undone.`,
      title: count === 1 ? "Delete chat" : "Delete chats",
      okLabel: count === 1 ? "Delete" : `Delete ${count}`,
    };
  }

  if (count === 1) {
    const lines = [
      "Delete this chat and its full history? This cannot be undone.",
      processes.length === 1
        ? "1 background command is running in this chat and will stop when the chat is deleted:"
        : `${processes.length} background commands are running in this chat and will stop when the chat is deleted:`,
      ...processes.map(
        (proc) => `• ${formatBackgroundCommandLine(proc.command, proc.ports)}`,
      ),
    ];
    return {
      title: "Delete chat",
      message: lines.join("\n"),
      okLabel: "Delete and stop",
      ...(options.onOpenRunning ? { onOpenRunning: options.onOpenRunning } : {}),
    };
  }

  const titleBySession = new Map<string, string>();
  for (const chat of options.chats ?? []) {
    titleBySession.set(chat.sessionId, sessionTitle(chat.title));
  }
  const groups = new Map<string, { title: string; items: RunningCommandSummary[] }>();
  for (const proc of processes) {
    const sid = proc.session_id ?? "";
    let group = groups.get(sid);
    if (!group) {
      group = {
        title:
          titleBySession.get(sid) ??
          sessionTitle(proc.session_title),
        items: [],
      };
      groups.set(sid, group);
    }
    group.items.push(proc);
  }

  const lines = [
    `Delete ${count} chats and their full history? This cannot be undone.`,
    processes.length === 1
      ? "1 background command is running and will stop when these chats are deleted:"
      : `${processes.length} background commands are running and will stop when these chats are deleted:`,
  ];
  for (const group of groups.values()) {
    lines.push(`${group.title}:`);
    for (const proc of group.items) {
      lines.push(`• ${formatBackgroundCommandLine(proc.command, proc.ports)}`);
    }
  }

  return {
    title: "Delete chats",
    message: lines.join("\n"),
    okLabel: "Delete and stop",
    ...(options.onOpenRunning ? { onOpenRunning: options.onOpenRunning } : {}),
  };
}

/** Confirm for the one destructive chat verb. */
export async function confirmChatDelete(
  input: number | ChatDeleteConfirmOptions = 1,
): Promise<boolean> {
  if (typeof input === "number") {
    return confirmDestructive(
      buildChatDeleteConfirmRequest({ count: input, processes: [] }),
    );
  }

  const chats = input.chats ?? [];
  const count = input.count ?? Math.max(1, chats.length);
  const confirm = input.confirm ?? confirmDestructive;
  const hasLiveQuery =
    Boolean(input.client) &&
    chats.length > 0 &&
    (typeof input.client?.listProjectBackgroundProcesses === "function" ||
      typeof input.client?.listSessionBackgroundProcesses === "function");

  let snapshot: readonly RunningCommandSummary[] =
    input.processes ??
    (hasLiveQuery
      ? await fetchChatRunningCommands(input.client, input.projectId, chats)
      : []);

  for (;;) {
    const ok = await confirm(
      buildChatDeleteConfirmRequest({
        count,
        chats,
        processes: snapshot,
        onOpenRunning: input.onOpenRunning,
      }),
    );
    if (!ok) return false;
    if (!hasLiveQuery) return true;
    const latest = await fetchChatRunningCommands(
      input.client,
      input.projectId,
      chats,
    );
    if (sameRunningCommandList(snapshot, latest)) {
      return true;
    }
    snapshot = latest;
  }
}

export type { ProjectBackgroundProcess };
