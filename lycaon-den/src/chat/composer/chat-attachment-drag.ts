import type { ChatAttachmentRef } from "./add-to-chat.ts";

export const CHAT_ATTACHMENT_DRAG_TYPE =
  "application/x-painted-wolf-chat-attachment";

type DropHandlers = {
  onDragActive: (active: boolean) => void;
  onDrop: (ref: ChatAttachmentRef) => void;
};

const targets = new Map<HTMLElement, DropHandlers>();
const activeDragTargets = new Set<HTMLElement>();
let pointerTarget: HTMLElement | null = null;

function requiredString(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const normalized = value.trim();
  return normalized || null;
}

function optionalLine(value: unknown): number | undefined | null {
  if (value === undefined) return undefined;
  return typeof value === "number" && Number.isInteger(value) && value >= 1
    ? value
    : null;
}

function parseChatAttachmentRef(value: unknown): ChatAttachmentRef | null {
  if (typeof value !== "object" || value === null) return null;
  const ref = value as Record<string, unknown>;
  const projectId = requiredString(ref.projectId);
  const name = requiredString(ref.name);
  if (!projectId || !name) return null;
  switch (ref.kind) {
    case "path-file": {
      const rootId = requiredString(ref.rootId);
      const path = requiredString(ref.path);
      const startLine = optionalLine(ref.startLine);
      const endLine = optionalLine(ref.endLine);
      if (!rootId || !path || startLine === null || endLine === null) return null;
      if (endLine !== undefined && startLine === undefined) return null;
      return {
        kind: "path-file",
        projectId,
        rootId,
        path,
        name,
        ...(startLine === undefined ? {} : { startLine }),
        ...(endLine === undefined ? {} : { endLine }),
      };
    }
    case "path-folder": {
      const rootId = requiredString(ref.rootId);
      const path = requiredString(ref.path);
      return rootId && path
        ? { kind: "path-folder", projectId, rootId, path, name }
        : null;
    }
    case "artifact": {
      const artifactId = requiredString(ref.artifactId);
      const previewUrl =
        ref.previewUrl === undefined ? undefined : requiredString(ref.previewUrl);
      if (!artifactId || previewUrl === null) return null;
      return {
        kind: "artifact",
        projectId,
        artifactId,
        name,
        ...(previewUrl === undefined ? {} : { previewUrl }),
      };
    }
    case "search-hit": {
      const sessionId = requiredString(ref.sessionId);
      const sourceRef = requiredString(ref.sourceRef);
      const hitKind = requiredString(ref.hitKind);
      return sessionId && sourceRef && hitKind
        ? {
            kind: "search-hit",
            projectId,
            sessionId,
            sourceRef,
            hitKind,
            name,
          }
        : null;
    }
    default:
      return null;
  }
}

function hasAttachmentType(event: DragEvent): boolean {
  const types = event.dataTransfer?.types;
  return Boolean(types && Array.from(types).includes(CHAT_ATTACHMENT_DRAG_TYPE));
}

function readAttachment(event: DragEvent): ChatAttachmentRef | null {
  const raw = event.dataTransfer?.getData(CHAT_ATTACHMENT_DRAG_TYPE) ?? "";
  if (!raw) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    return parseChatAttachmentRef(parsed);
  } catch {
    return null;
  }
}

export function startChatAttachmentDrag(
  event: DragEvent,
  ref: ChatAttachmentRef | null | undefined,
): void {
  if (!ref || !event.dataTransfer) {
    event.preventDefault();
    return;
  }
  event.dataTransfer.effectAllowed = "copy";
  event.dataTransfer.setData(CHAT_ATTACHMENT_DRAG_TYPE, JSON.stringify(ref));
  const source = event.currentTarget;
  if (source instanceof HTMLElement) {
    source.addEventListener(
      "dragend",
      () => {
        for (const target of activeDragTargets) {
          targets.get(target)?.onDragActive(false);
        }
        activeDragTargets.clear();
      },
      { once: true },
    );
  }
}

export function registerChatAttachmentDrop(
  target: HTMLElement,
  handlers: DropHandlers,
): () => void {
  targets.set(target, handlers);
  let depth = 0;

  const enter = (event: DragEvent) => {
    if (!hasAttachmentType(event)) return;
    event.preventDefault();
    depth += 1;
    activeDragTargets.add(target);
    handlers.onDragActive(true);
  };
  const over = (event: DragEvent) => {
    if (!hasAttachmentType(event)) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  };
  const leave = (event: DragEvent) => {
    if (!hasAttachmentType(event)) return;
    event.preventDefault();
    depth = Math.max(0, depth - 1);
    if (depth === 0) {
      activeDragTargets.delete(target);
      handlers.onDragActive(false);
    }
  };
  const drop = (event: DragEvent) => {
    if (!hasAttachmentType(event)) return;
    event.preventDefault();
    depth = 0;
    activeDragTargets.delete(target);
    handlers.onDragActive(false);
    const ref = readAttachment(event);
    if (ref) handlers.onDrop(ref);
  };

  target.addEventListener("dragenter", enter);
  target.addEventListener("dragover", over);
  target.addEventListener("dragleave", leave);
  target.addEventListener("drop", drop);
  return () => {
    target.removeEventListener("dragenter", enter);
    target.removeEventListener("dragover", over);
    target.removeEventListener("dragleave", leave);
    target.removeEventListener("drop", drop);
    targets.delete(target);
    activeDragTargets.delete(target);
    if (pointerTarget === target) {
      handlers.onDragActive(false);
      pointerTarget = null;
    }
  };
}

function registeredTargetAt(x: number, y: number): HTMLElement | null {
  if (typeof document.elementFromPoint !== "function") return null;
  let node: Element | null = document.elementFromPoint(x, y);
  while (node instanceof HTMLElement) {
    if (targets.has(node)) return node;
    node = node.parentElement;
  }
  return null;
}

export function updatePointerChatAttachmentDrag(x: number, y: number): boolean {
  const next = registeredTargetAt(x, y);
  if (next === pointerTarget) return next !== null;
  if (pointerTarget) targets.get(pointerTarget)?.onDragActive(false);
  pointerTarget = next;
  if (pointerTarget) targets.get(pointerTarget)?.onDragActive(true);
  return pointerTarget !== null;
}

export function finishPointerChatAttachmentDrag(
  ref: ChatAttachmentRef,
  x: number,
  y: number,
): boolean {
  const target = registeredTargetAt(x, y);
  if (pointerTarget) targets.get(pointerTarget)?.onDragActive(false);
  pointerTarget = null;
  if (!target) return false;
  targets.get(target)?.onDrop(ref);
  return true;
}

export function cancelPointerChatAttachmentDrag(): void {
  if (pointerTarget) targets.get(pointerTarget)?.onDragActive(false);
  pointerTarget = null;
}
