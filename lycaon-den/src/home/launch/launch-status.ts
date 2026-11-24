import type { LaunchRecord } from "./home-launch-model.ts";
import type { LaunchBlocker, LaunchRuntime, LaunchStep } from "./home-launches.ts";

/** Actions a launch can offer; the surface binds each to its handler. */
export type LaunchAction =
  | "retry"
  | "retry_saving"
  | "copy"
  | "send_here"
  | "open_settings"
  | "open_chat"
  | "edit_on_home"
  | "start_again"
  | "remove";

export type LaunchNotice = {
  tone: "info" | "warning" | "danger";
  title: string;
  body?: string;
  primary?: { action: LaunchAction; label: string };
  secondary?: { action: LaunchAction; label: string };
};

const STEP_COPY: Record<LaunchStep, string> = {
  starting: "Starting chat…",
  creating: "Creating your chat…",
  preparing: "Preparing the workspace…",
  attaching: "Attaching files…",
  sending: "Sending…",
  sent: "Sent",
};

export function launchStepText(step: LaunchStep): string {
  return STEP_COPY[step];
}

function reference(record: LaunchRecord): string {
  return `Reference ${record.launchId.slice(0, 8)}.`;
}

/** What the chat says about a blocked launch and which actions resolve it. */
export function launchNotice(record: LaunchRecord, blocker: LaunchBlocker): LaunchNotice {
  const hasChat = Boolean(record.hostState?.session_id);
  switch (blocker.code) {
    case "engine_offline":
      return {
        tone: "info",
        title: "Waiting for Painted Wolf Code",
        body: "Your idea is kept here and sends when the engine is back.",
        primary: { action: "retry", label: "Retry now" },
      };
    case "transport_failed":
      return {
        tone: "warning",
        title: "Couldn't reach the engine",
        body: blocker.message,
        primary: { action: "retry", label: "Retry" },
        secondary: { action: "copy", label: "Copy idea" },
      };
    case "host_outdated":
      return {
        tone: "warning",
        title: "This engine can't start chats from Home",
        body: "Update or restart Painted Wolf Code, then retry. Your idea stays here.",
        primary: { action: "retry", label: "Retry" },
        secondary: { action: "copy", label: "Copy idea" },
      };
    case "destination_changed":
      return {
        tone: "warning",
        title: "This idea was written for a different engine",
        body: "Send it to the engine you're connected to now, or copy it.",
        primary: { action: "send_here", label: "Send here" },
        secondary: { action: "copy", label: "Copy idea" },
      };
    case "attachments":
      return {
        tone: "warning",
        title: "Fix the marked files to send",
        body: "Choose them again or remove them. The rest of your idea is kept.",
      };
    case "host_failed":
      if (blocker.failure === "draft_workspace_occupied") {
        return {
          tone: "danger",
          title: "Couldn't create this chat's draft folder",
          body: "Unexpected files are in its place and were left untouched. Start again to use a new folder.",
          primary: { action: "start_again", label: "Start again" },
          secondary: { action: "copy", label: "Copy idea" },
        };
      }
      return {
        tone: "danger",
        title: "Couldn't create this chat",
        body: blocker.resumable ? "Your idea is kept. Retry when ready." : reference(record),
        primary: blocker.resumable ? { action: "retry", label: "Retry" } : { action: "start_again", label: "Start again" },
        secondary: { action: "copy", label: "Copy idea" },
      };
    case "chat_failed":
      return {
        tone: "danger",
        title: "This chat couldn't finish preparing",
        body: "Open it to see what happened. Your idea was not sent.",
        primary: { action: "open_chat", label: "Open chat" },
        secondary: { action: "copy", label: "Copy idea" },
      };
    case "provider":
      return {
        tone: "warning",
        title: "Choose a model to send this idea",
        body: blocker.message,
        primary: { action: "open_settings", label: "Open model settings" },
        secondary: { action: "retry", label: "Retry" },
      };
    case "send_refused":
      return {
        tone: "danger",
        title: "The engine didn't accept this idea",
        body: blocker.message,
        primary: { action: "retry", label: "Retry" },
        secondary: { action: "copy", label: "Copy idea" },
      };
    case "stopped":
      return {
        tone: "info",
        title: "Stopped",
        body: "Nothing was sent. Edit the idea on Home or remove it.",
        primary: { action: "edit_on_home", label: "Edit on Home" },
        secondary: hasChat ? { action: "open_chat", label: "Open chat" } : { action: "remove", label: "Remove" },
      };
    case "retired":
      return {
        tone: "info",
        title: "This chat was deleted",
        body: "Nothing more will be sent from this idea.",
        primary: { action: "edit_on_home", label: "Edit on Home" },
        secondary: { action: "remove", label: "Remove" },
      };
    case "unknown":
      return {
        tone: "danger",
        title: "Something went wrong",
        body: `${blocker.message} ${reference(record)}`.trim(),
        primary: { action: "retry", label: "Retry" },
        secondary: { action: "copy", label: "Copy idea" },
      };
  }
}

/** The save notice, shown beside the idea while local storage fails. */
export function saveNotice(runtime: LaunchRuntime): LaunchNotice | undefined {
  if (runtime.save !== "failed") return undefined;
  return {
    tone: "warning",
    title: "Couldn't save this idea on this device",
    body: `${runtime.saveError ?? ""} It stays here while the app is open.`.trim(),
    primary: { action: "retry_saving", label: "Retry saving" },
    secondary: { action: "copy", label: "Copy idea" },
  };
}

/** A short state for Home's list of ideas still on their way to a chat. */
export function launchSummary(runtime: LaunchRuntime | undefined): string {
  if (!runtime) return "Starting";
  if (runtime.blocker?.code === "stopped") return "Stopped";
  if (runtime.blocker?.code === "engine_offline") return "Waiting for the engine";
  if (runtime.blocker) return "Needs attention";
  return "Starting";
}
