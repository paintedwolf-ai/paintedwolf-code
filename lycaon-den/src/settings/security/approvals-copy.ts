import type { ApprovalSecretDestinationKind, ElevatedAccessEffect } from "../../api/types.ts";

const ELEVATED_EFFECT_LABELS: Record<ElevatedAccessEffect, string> = {
  direct_network: "direct network access",
  host_execution: "host execution",
  process_control: "process control",
  local_service: "local service access",
};

/** Static approval UI copy. */

export const APPROVALS_COPY = {
  elevated: {
    label: "Elevated access",
    action: (count: number) => `Review ${count} elevated-access approval${count === 1 ? "" : "s"} in Project configuration`,
    tooltip: (count: number) => `Review ${count} elevated-access approval${count === 1 ? "" : "s"} in Project configuration → Approvals.`,
    loading: "Checking elevated-access approvals…",
    refreshFailed: "Could not check elevated-access approvals.",
    unavailable: "Elevated-access approvals are unavailable. Reconnect to review them.",
  },
  revokeButton: "Revoke",
  card: {
    elevated: {
      label: "Elevated access",
      tooltip: (effects: readonly ElevatedAccessEffect[]) =>
        `Saving an approval on this card can allow ${effects.map((effect) => ELEVATED_EFFECT_LABELS[effect]).join(", ")}. The unlock icon appears in the composer while that saved access is active. Allow once does not save access.`,
    },
    /** Eyebrow grammar: `{State} · {Action phrase}` — state from a closed set. */
    state: {
      needsApproval: "Needs approval",
      editReview: "Edit review",
    },
    action: {
      runCommand: "Run command",
      useTool: (tool: string) => `Use ${tool}`,
      applyChanges: "Apply changes",
    },
    /** Primary verbs: Allow for permissions, Apply for bytes; kind-specific only
     * when the verb is the meaning. */
    allow: "Allow",
    otherOptions: "Other choices",
    apply: "Apply",
    /** Decline uses the full danger-button style. */
    no: "No",
    noArmed: "No — send instruction",
    noAssurance: "No stops only this action — the chat keeps going.",
    viewDiff: "View diff",
    secretShapeLabel: "Looks like",
    protectedValuesLabel: "Protected values",
    /** Destination kind labels. */
    destinationKind: {
      model_provider: "your model provider — the model reads this",
      service: "service",
      process: "destinations not observed",
      file: "file",
    } as const satisfies Record<ApprovalSecretDestinationKind, string>,
    /** Host presentation.command when the subject is a path/host/secret, not the argv. */
    causedByLabel: "From",
    locationSeparator: "→",
    showInChat: "Show in chat",
    /** Grant split-button and drop-up. Titles and coverage are host-authored. */
    grantMenu: {
      ariaLabel: "Allow with a grant",
      moreLabel: "More durations",
      reask: (when: string) => `Asks again if ${when.replace(/\.$/, "")}.`,
      /** Slot digits are the same gesture on every card: 3 is this chat. */
      slotKey: (n: number) => String(n),
      slotAria: (n: number, title: string) => `${n}: ${title}`,
      unavailable: "Not available",
      applyAndSkip: "Apply and skip…",
      skipDay: "For 1 day",
      skipDayMeta: "this path",
      skipAlways: "Always",
      skipAlwaysMeta: "review setting",
    },
    /** Coverage and expiry of the face option, under the action row. */
    faceMeta: (coverage: string, expiresWhen: string) =>
      expiresWhen ? `${coverage} · ${expiresWhen}` : coverage,
    /** How long a card has been waiting; shown from one minute on. */
    waiting: (age: string) => `Waiting ${age}`,
    /** Minimize affordance on the docked card. */
    collapse: {
      minimizeLabel: "Minimize approval",
      expandLabel: "Expand approval",
    },
    /** Transcript stand-in while the interactive card is docked above the composer. */
    openMarker: "Waiting for your decision in the composer…",
    /** Compact waiting rows above the one expanded card. */
    queue: {
      ariaLabel: "Approvals waiting",
    },
    /** Dock overflow line — queued approvals beyond the visible rows. */
    dockMore: (n: number) =>
      n === 1 ? "1 more approval waiting" : `${n} more approvals waiting`,
    /** Optional guidance for redirecting the pending action. */
    rail: {
      lead: "Or tell the agent what to do instead —",
      typeBelow: "type below",
      tail: "and Send.",
      armed: "Send will deny this and pass your instruction to the agent.",
    },
    composerRedirect: {
      title: "Directing the agent",
      detail: "Send denies the action above and passes your instruction.",
      placeholder: "Tell the agent what to do instead…",
    },
    /** Labels agent-authored context. */
    agentLine: {
      label: "Agent",
      pending: "Summarizing how this fits the goal…",
      disclaimer:
        "AI-generated to add context. It can be incomplete or wrong — base your decision on the action above.",
    },
    /** Full target sets open in Files. */
    targetSet: {
      show: (count: number, kind?: string) => {
        const noun =
          kind === "destination_set"
            ? "destinations"
            : kind === "action_set"
              ? "actions"
              : kind === "socket_set"
                ? "local services"
                : kind === "write_root_set"
                  ? "write locations"
                  : kind === "package_set"
                    ? "packages"
                  : "items";
        return `Show all ${count} ${noun}`;
      },
    },
    packageIdentity: {
      resolved: "Registry identity resolved",
      notFound: "Package or version not found in the registry identity index — this approval will not be reused",
      unavailable: "Registry identity unavailable — this approval will not be reused",
      unsupported: "Registry identity is not supported — this approval will not be reused",
      publishedToday: "published today",
      publishedDaysAgo: (days: number) => `published ${days} days ago`,
      verifiedSource: "source attestation verified",
      executionBoundary: (hosts: readonly string[], readPaths: readonly string[] = []) => {
        const reads = readPaths.length > 0
          ? `Approved read access to ${readPaths.join(", ")}; other protected reads require approval.`
          : "Protected reads require approval.";
        const network = hosts.length > 0
          ? `Network only to ${hosts.join(", ")}.`
          : "Network is limited to the reviewed package source.";
        return `Runs without inherited credentials. ${reads} ${network}`;
      },
    },
    details: {
      summary: "Details",
      whoLabel: "Who",
      ifWrongLabel: "If wrong",
      flaggedByLabel: "Flagged by",
      policyLabel: "Policy",
      alsoPolicyLabel: "Also policy",
      /** The primary host gate, named plainly. */
      whyLabel: "Why you were asked",
      /** A secondary gate that fired on the same action. */
      alsoLabel: "Also",
      /** Verbatim host evidence. */
      observedLabel: "Observed",
      declaredDestinationsLabel: "Declared destinations",
      directIPVisibilityLabel: "Visibility",
      directIPUnobserved:
        "Unobserved — these destinations were declared by the agent, not observed by the app.",
      directIPUndeclared:
        "None — direct network access is not narrowed to declared ports.",
      resolvedSocketPathLabel: "Resolved service path",
    },
    /** Additional waiters reported by the host. */
    identicalCallsWaiting: (n: number) =>
      `${n} identical calls waiting on this approval.`,
    /** Repeated reasons reported by the host. */
    repeat: {
      summary: (n: number) =>
        `Asked ${n} times for this reason since your last message · allow and stop asking below`,
      /** Repeat metadata explains the ask without changing the primary choice. */
      again: "Asked again for the same reason. Allowing for this chat stops the ask.",
      scopeNote:
        "Each was decided separately. This card decides only the action above.",
      truncated: (n: number) =>
        n === 1 ? "…and 1 more" : `…and ${n} more`,
      suppressed: (n: number) =>
        n === 1
          ? "1 more was blocked without showing a card."
          : `${n} more were blocked without showing a card.`,
    },
    /** Risk styling follows the host's consequence fields. */
    highRisk: {
      label: "High risk",
      consequence: {
        write_root:
          "This grants write access to a directory programs are launched from.",
        local_socket:
          "The service runs outside the sandbox with your permissions. It may act on files, processes, devices, or the network on this command's behalf, and those inner actions may not be visible here.",
        direct_ip:
          "The app cannot observe or check the destinations this command reaches.",
      },
    },
    decision: {
      tool_approval: {
        approved: "Allowed",
        rejected: "Denied",
        expired: "Timed out",
        canceled: "Canceled by stop",
        pending: "Needs approval",
      },
      content_apply: {
        approved: "Edit applied",
        rejected: "Edit rejected",
        expired: "Edit timed out",
        canceled: "Edit canceled by stop",
        pending: "Edit review",
      },
      fallback: {
        approved: "Approved",
        rejected: "Denied",
        expired: "Timed out",
        canceled: "Canceled by stop",
        pending: "Needs approval",
      },
    },
    /** Direction shown after a rejection is resolved. */
    rejectGuidance: {
      directionLabel: "Direction sent",
    },
  },
} as const;
