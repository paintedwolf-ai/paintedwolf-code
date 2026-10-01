/** One explicit destination boundary for Add-to-chat actions and chat drops. */

import { createSignal } from "solid-js";
import {
  byteAttachmentIntakeReject,
  fileToComposerAttachment,
  mediaMimeFromPath,
  composerAttachmentCapabilities,
  attachmentReceiptToPending,
  attachmentRejectCode,
  rejectMessageForCode,
  textToComposerAttachment,
  type ComposerPendingArtifact,
  type ComposerPendingAttachment,
  type ComposerPendingPathFile,
  type ComposerPendingPathFolder,
  type ComposerPendingSearchHit,
} from "./composer-attachments.ts";
import { pendingAttachmentsForSession } from "./composer-attachment-store.ts";
import { publishNotice } from "../../notices/notice-store.ts";
import { projectScope } from "../../notices/notice-scope.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { chooseChatDestination } from "./chat-destination.ts";
import { stageComposerMutation } from "./composer-document-store.ts";
import type { ComposerBlockReason } from "./composer-rules.ts";
import { normalizeLineRange, samePathFileRange } from "./path-file-ref.ts";
import {
  formatSelectedTextAttachmentBody,
  resolveSelectionProvenance,
  selectionAttachmentFilename,
  type SelectionProvenance,
} from "./selection-provenance.ts";
import type { ChatDestination } from "./shared-composer-document.ts";
import { classifyDroppedItem, type DropClassification, type DropEvent, type DroppedItem } from "../../platform/files/file-drop.ts";
import { basenameOfPath, relativeUnderRoot } from "../../api/project-path.ts";
import { pathKind } from "../../platform/files/path-kind.ts";
import { importPathKind, readPathBytes } from "../../platform/files/read-path-bytes.ts";
import { importExternalAttachment } from "../../platform/files/external-attachment-import.ts";
import { pathIsUnderProjectRoots } from "../../platform/files/reveal-in-file-manager.ts";
import { tauriPlatform } from "../../platform/runtime.ts";
import {
  longestMatchingRoot,
  resolveProjectFile,
  type ResolveProjectRoot,
} from "../../api/project-path.ts";

export type ChatAttachmentRef =
  | Omit<ComposerPendingPathFile, "id">
  | Omit<ComposerPendingPathFolder, "id">
  | Omit<ComposerPendingArtifact, "id">
  | Omit<ComposerPendingSearchHit, "id">;

export type ComposerAttachmentSink = {
  sessionId: () => string;
  blockReason: () => ComposerBlockReason;
  projectId: () => string;
  projectRoots: () => readonly ResolveProjectRoot[];
  reveal: () => void;
};

export type AddToChatOptions = {
  /** Chat-scoped actions and drops provide this exact machine-state target. */
  destination?: ChatDestination;
  /** Compound editor actions apply this to the same destination when empty. */
  draftPrefill?: string;
};

export type AddToChatResult =
  | { ok: true; destination: ChatDestination }
  | { ok: false; reason: string };

let attachmentSink: ComposerAttachmentSink | null = null;
const [attachFocusPulse, setAttachFocusPulse] = createSignal(0);

export function composerAttachFocusPulse(): number {
  return attachFocusPulse();
}

export function registerComposerAttachmentSink(
  sink: ComposerAttachmentSink,
): () => void {
  attachmentSink = sink;
  return () => {
    if (attachmentSink === sink) attachmentSink = null;
  };
}

function refuseForBlock(block: ComposerBlockReason): string | null {
  switch (block) {
    case "offline":
      return "Connect to the backend to attach files.";
    case "no_provider":
      return "Add a model provider before attaching.";
    case "no_session":
      return "Open or choose a chat to attach files.";
    // Soft send holds — stage chips into the draft; Send waits separately.
    case "session_preparing":
    case "workflow_paused":
    default:
      return null;
  }
}

export function composerAttachBlockMessage(block: ComposerBlockReason): string {
  return refuseForBlock(block) ?? "Composer is unavailable.";
}

function sinkDestination(): ChatDestination | undefined {
  const projectId = attachmentSink?.projectId().trim() ?? "";
  const sessionId = attachmentSink?.sessionId().trim() ?? "";
  return projectId && sessionId ? { projectId, sessionId } : undefined;
}

async function resolveDestination(
  requiredProjectId: string | undefined,
  explicit: ChatDestination | undefined,
): Promise<ChatDestination | null> {
  if (explicit) return explicit;
  const candidate = sinkDestination();
  const suggested =
    candidate && (!requiredProjectId || candidate.projectId === requiredProjectId)
      ? candidate
      : undefined;
  return chooseChatDestination({ projectId: requiredProjectId, suggested });
}

function revealDestination(destination: ChatDestination): void {
  const local = sinkDestination();
  if (
    local?.projectId !== destination.projectId ||
    local.sessionId !== destination.sessionId
  ) {
    return;
  }
  attachmentSink?.reveal();
  setAttachFocusPulse((pulse) => pulse + 1);
}

function localRefusal(destination: ChatDestination): string | null {
  const local = sinkDestination();
  if (
    local?.projectId !== destination.projectId ||
    local.sessionId !== destination.sessionId
  ) {
    return null;
  }
  return refuseForBlock(attachmentSink?.blockReason() ?? null);
}

export async function addToChat(
  ref: ChatAttachmentRef,
  options: AddToChatOptions = {},
): Promise<AddToChatResult> {
  const destination = await resolveDestination(
    ref.projectId.trim(),
    options.destination,
  );
  if (!destination) return { ok: false, reason: "No chat chosen." };
  const refusal = localRefusal(destination);
  if (refusal) return { ok: false, reason: refusal };

  let pending: ComposerPendingAttachment;
  if (ref.kind === "path-file") {
    const range = normalizeLineRange(ref.startLine, ref.endLine);
    const normalized = {
      ...ref,
      startLine: range?.startLine,
      endLine: range?.endLine,
    };
    if (
      pendingAttachmentsForSession(destination.sessionId).some(
        (attachment) =>
          attachment.kind === "path-file" &&
          samePathFileRange(attachment, normalized),
      )
    ) {
      revealDestination(destination);
      return { ok: true, destination };
    }
    pending = { id: crypto.randomUUID(), ...normalized };
  } else {
    pending = { id: crypto.randomUUID(), ...ref };
  }

  const result = await stageComposerMutation(
    destination,
    [pending],
    options.draftPrefill,
  );
  if (!result.ok) return result;
  revealDestination(destination);
  return { ok: true, destination };
}

/** Stages one text attachment at one chosen destination without changing the draft. */
export async function addTextAttachmentToChat(
  projectId: string,
  text: string,
  filename: string,
): Promise<AddToChatResult> {
  if (!text.trim()) return { ok: false, reason: "Nothing selected." };
  const destination = await resolveDestination(projectId.trim(), undefined);
  if (!destination) return { ok: false, reason: "No chat chosen." };
  const refusal = localRefusal(destination);
  if (refusal) return { ok: false, reason: refusal };
  const client = getLycaonClient();
  if (!client) return { ok: false, reason: "Not connected to the host." };
  const pending = await textToComposerAttachment(text, filename, destination.projectId, client);
  if (pending.kind === "reject") {
    return {
      ok: false,
      reason: pending.rejectMessage || rejectMessageForCode(pending.rejectCode ?? "attachment_unavailable"),
    };
  }
  const result = await stageComposerMutation(destination, [pending]);
  if (!result.ok) return result;
  revealDestination(destination);
  return { ok: true, destination };
}

/** Prefill the composer draft with no attachment. Never clobbers user text. */
export async function prefillComposerDraft(
  projectId: string,
  text: string,
): Promise<AddToChatResult> {
  const destination = await resolveDestination(projectId.trim(), undefined);
  if (!destination) return { ok: false, reason: "No chat chosen." };
  const refusal = localRefusal(destination);
  if (refusal) return { ok: false, reason: refusal };
  const result = await stageComposerMutation(destination, [], text);
  if (!result.ok) return result;
  revealDestination(destination);
  return { ok: true, destination };
}

/** Prefill failures surface as notices for the active project. */
export async function runComposerPrefillEffect(
  projectId: string | undefined | null,
  text: string,
): Promise<void> {
  if (!projectId) {
    publishNotice({ title: "Couldn't fill the composer", message: "No project is open." });
    return;
  }
  const result = await prefillComposerDraft(projectId, text);
  if (!result.ok) {
    publishNotice(
      { title: "Couldn't fill the composer", message: result.reason },
      projectScope(projectId),
    );
  }
}

export async function addSelectedTextToChat(
  text: string,
  target: EventTarget | null = null,
  provenance?: SelectionProvenance,
  options: AddToChatOptions = {},
): Promise<AddToChatResult> {
  const snippet = text.replace(/\s+$/u, "");
  if (snippet.trim().length === 0) {
    return { ok: false, reason: "Nothing selected." };
  }

  const prov = provenance ?? resolveSelectionProvenance(target, snippet);
  prov.text = snippet;
  const roots = attachmentSink?.projectRoots() ?? [];
  const sinkProjectId = attachmentSink?.projectId().trim() ?? "";
  if (prov.path && !prov.projectId && sinkProjectId) {
    prov.projectId = sinkProjectId;
  }
  if (prov.path && !prov.rootId && roots.length > 0) {
    const resolved = resolveProjectFile({ roots }, prov.path);
    if (!("error" in resolved)) {
      const containingRoot = longestMatchingRoot(resolved.absolutePath, roots);
      if (containingRoot) {
        prov.rootId = containingRoot.id;
        const relative = relativeUnderRoot(resolved.absolutePath, containingRoot.path);
        if (relative != null && relative !== ".") prov.path = relative;
      }
    }
  }

  const destination = await resolveDestination(
    prov.projectId?.trim() || undefined,
    options.destination ??
      (prov.projectId?.trim() && prov.sessionId?.trim()
        ? {
            projectId: prov.projectId.trim(),
            sessionId: prov.sessionId.trim(),
          }
        : undefined),
  );
  if (!destination) return { ok: false, reason: "No chat chosen." };
  const refusal = localRefusal(destination);
  if (refusal) return { ok: false, reason: refusal };

  // The body is stored against the chat it is going to, not against wherever
  // the text came from: ungrounded selections have no project of their own.
  const client = getLycaonClient();
  if (!client) {
    return { ok: false, reason: "Not connected to the host." };
  }
  const content = formatSelectedTextAttachmentBody(prov, snippet);
  const attachments: ComposerPendingAttachment[] = [
    await textToComposerAttachment(
      content,
      selectionAttachmentFilename(prov.path),
      destination.projectId,
      client,
    ),
  ];
  // The path reference stays keyed to where the text actually lives, which is
  // not necessarily the chat's project.
  const projectId = prov.projectId?.trim() ?? "";
  const rootId = prov.rootId?.trim() ?? "";
  const relativePath = prov.path?.trim() ?? "";
  if (projectId && rootId && relativePath) {
    attachments.push({
      id: crypto.randomUUID(),
      kind: "path-file",
      projectId,
      rootId,
      path: relativePath,
      name: basenameOfPath(relativePath),
    });
  }

  const result = await stageComposerMutation(
    destination,
    attachments,
    options.draftPrefill,
  );
  if (!result.ok) return result;
  revealDestination(destination);
  return { ok: true, destination };
}

function rejectChip(reason: string, name = "attachment"): ComposerPendingAttachment {
  return {
    id: crypto.randomUUID(),
    kind: "reject",
    name,
    mime: "application/octet-stream",
    byteLength: 0,
    rejectMessage: reason,
  };
}

/** Caps verdict against what this destination already has staged. */
function byteAttachmentPreflight(
  destination: ChatDestination,
  byteLength?: number,
  mime?: string,
): string | null {
  return byteAttachmentIntakeReject(
    pendingAttachmentsForSession(destination.sessionId),
    byteLength,
    mime,
  );
}

async function stageFile(
  file: File,
  destination: ChatDestination,
): Promise<ComposerPendingAttachment> {
  const preflight = byteAttachmentPreflight(destination, file.size, file.type);
  if (preflight) return rejectChip(preflight, file.name);
  const client = getLycaonClient();
  if (!client || !destination.projectId) {
    return rejectChip(rejectMessageForCode("attachment_unavailable"), file.name);
  }
  return fileToComposerAttachment(file, destination.projectId, client);
}

export async function ingestDropClassification(
  classification: DropClassification,
  destination: ChatDestination,
  rootPaths: readonly string[],
): Promise<{ ok: true } | { ok: false; reason: string }> {
  switch (classification.kind) {
    case "reject":
      return stageComposerMutation(destination, [rejectChip(classification.reason)]);
    case "path-file": {
      const result = await addToChat(
        {
          kind: "path-file",
          projectId: classification.projectId,
          rootId: classification.rootId,
          path: classification.path,
          name: basenameOfPath(classification.path),
        },
        { destination },
      );
      return result.ok ? { ok: true } : result;
    }
    case "path-folder": {
      const result = await addToChat(
        {
          kind: "path-folder",
          projectId: classification.projectId,
          rootId: classification.rootId,
          path: classification.path,
          name: basenameOfPath(classification.path),
        },
        { destination },
      );
      return result.ok ? { ok: true } : result;
    }
    case "web-file": {
      const pending = await stageFile(classification.file, destination);
      return stageComposerMutation(destination, [pending]);
    }
    case "external-file":
      try {
        const preflight = byteAttachmentPreflight(
          destination,
          undefined,
          mediaMimeFromPath(classification.absolutePath),
        );
        if (preflight) {
          return stageComposerMutation(destination, [
            rejectChip(preflight, basenameOfPath(classification.absolutePath)),
          ]);
        }
        const receipt = await importExternalAttachment(
          destination.projectId,
          classification.absolutePath,
        );
        return stageComposerMutation(destination, [
          attachmentReceiptToPending(receipt, { importedCopy: true }),
        ]);
      } catch (error) {
        return stageComposerMutation(destination, [
          rejectChip(
            rejectMessageForCode(attachmentRejectCode(error)),
            basenameOfPath(classification.absolutePath),
          ),
        ]);
      }
    case "bytes-media":
      try {
        const bytes = await readPathBytes(
          classification.absolutePath,
          rootPaths,
          composerAttachmentCapabilities()?.max_upload_bytes ?? 0,
        );
        const mime = mediaMimeFromPath(classification.absolutePath) ?? "application/octet-stream";
        const name = basenameOfPath(classification.absolutePath);
        const file = new File([new Uint8Array(bytes)], name, { type: mime });
        return stageComposerMutation(destination, [
          await stageFile(file, destination),
        ]);
      } catch (error) {
        return stageComposerMutation(destination, [
          rejectChip(
            rejectMessageForCode(attachmentRejectCode(error)),
            basenameOfPath(classification.absolutePath),
          ),
        ]);
      }
  }
}

export async function ingestChatDrop(event: DropEvent): Promise<{
  attached: number;
  refusedReason?: string;
}> {
  const sink = attachmentSink;
  if (!sink) return { attached: 0, refusedReason: "Composer is unavailable." };
  const block = sink.blockReason();
  if (block != null) {
    const refusal = refuseForBlock(block);
    if (refusal) return { attached: 0, refusedReason: refusal };
  }
  const destination = sinkDestination();
  if (!destination) {
    return { attached: 0, refusedReason: "Open a chat to attach files." };
  }
  const roots = sink.projectRoots();
  const rootPaths = roots.map((root) => root.path).filter((path) => path.trim());
  const platform = tauriPlatform();
  let attached = 0;
  let lastFail: string | undefined;
  for (const item of event.items) {
    const classification = await classifyDropItem(
      item,
      destination.projectId,
      roots,
      platform,
    );
    const result = await ingestDropClassification(
      classification,
      destination,
      rootPaths,
    );
    if (result.ok) attached += 1;
    else lastFail = result.reason;
  }
  return { attached, refusedReason: lastFail };
}

async function classifyDropItem(
  item: DroppedItem,
  projectId: string,
  roots: readonly ResolveProjectRoot[],
  platform: ReturnType<typeof tauriPlatform>,
): Promise<DropClassification> {
  if (item.source === "file") {
    return classifyDroppedItem(item, projectId, roots, null, platform);
  }
  const rootPaths = roots.map((root) => root.path).filter((path) => path.trim());
  const inProject = pathIsUnderProjectRoots(item.absolutePath, rootPaths, platform);
  let kind: "file" | "folder" | "missing" = "missing";
  try {
    kind = inProject
      ? await pathKind(item.absolutePath, rootPaths)
      : await importPathKind(item.absolutePath);
  } catch {
    kind = "missing";
  }
  return classifyDroppedItem(item, projectId, roots, kind, platform);
}

export function resetComposerAttachmentSinkForTests(): void {
  attachmentSink = null;
  setAttachFocusPulse(0);
}
