// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CHAT_ATTACHMENT_DRAG_TYPE,
  cancelPointerChatAttachmentDrag,
  finishPointerChatAttachmentDrag,
  registerChatAttachmentDrop,
  startChatAttachmentDrag,
  updatePointerChatAttachmentDrag,
} from "./chat-attachment-drag.ts";

class DragData {
  effectAllowed = "none";
  dropEffect = "none";
  private readonly values = new Map<string, string>();

  get types(): string[] {
    return [...this.values.keys()];
  }

  setData(type: string, value: string): void {
    this.values.set(type, value);
  }

  getData(type: string): string {
    return this.values.get(type) ?? "";
  }
}

function dragEvent(type: string, dataTransfer: DragData): DragEvent {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
  return event as DragEvent;
}

const ref = {
  kind: "path-file" as const,
  projectId: "project-1",
  rootId: "root-1",
  path: "src/main.ts",
  name: "main.ts",
};

afterEach(() => cancelPointerChatAttachmentDrag());

describe("chat attachment drag", () => {
  it("serializes a typed item and drops it on the registered chat", () => {
    const target = document.createElement("section");
    const active = vi.fn();
    const drop = vi.fn();
    const detach = registerChatAttachmentDrop(target, {
      onDragActive: active,
      onDrop: drop,
    });
    const data = new DragData();

    startChatAttachmentDrag(dragEvent("dragstart", data), ref);
    expect(data.effectAllowed).toBe("copy");
    expect(data.types).toContain(CHAT_ATTACHMENT_DRAG_TYPE);

    target.dispatchEvent(dragEvent("dragenter", data));
    target.dispatchEvent(dragEvent("dragover", data));
    target.dispatchEvent(dragEvent("drop", data));

    expect(active).toHaveBeenNthCalledWith(1, true);
    expect(active).toHaveBeenLastCalledWith(false);
    expect(drop).toHaveBeenCalledWith(ref);
    detach();
  });

  it("rejects malformed renderer payloads", () => {
    const target = document.createElement("section");
    const drop = vi.fn();
    const detach = registerChatAttachmentDrop(target, {
      onDragActive: () => {},
      onDrop: drop,
    });
    const data = new DragData();
    data.setData(CHAT_ATTACHMENT_DRAG_TYPE, JSON.stringify({ kind: "path-file" }));

    target.dispatchEvent(dragEvent("drop", data));

    expect(drop).not.toHaveBeenCalled();
    detach();
  });

  it("projects renderer payloads onto the closed attachment shape", () => {
    const target = document.createElement("section");
    const drop = vi.fn();
    const detach = registerChatAttachmentDrop(target, {
      onDragActive: () => {},
      onDrop: drop,
    });
    const data = new DragData();
    data.setData(
      CHAT_ATTACHMENT_DRAG_TYPE,
      JSON.stringify({ ...ref, id: "injected", unexpected: true }),
    );

    target.dispatchEvent(dragEvent("drop", data));

    expect(drop).toHaveBeenCalledWith(ref);
    detach();
  });

  it("bridges pointer-driven file drags into the chat target", () => {
    const target = document.createElement("section");
    const child = document.createElement("div");
    target.append(child);
    document.body.append(target);
    const active = vi.fn();
    const drop = vi.fn();
    const detach = registerChatAttachmentDrop(target, {
      onDragActive: active,
      onDrop: drop,
    });
    const point = vi.fn(() => child);
    Object.defineProperty(document, "elementFromPoint", {
      configurable: true,
      value: point,
    });

    expect(updatePointerChatAttachmentDrag(20, 30)).toBe(true);
    expect(finishPointerChatAttachmentDrag(ref, 20, 30)).toBe(true);

    expect(active).toHaveBeenCalledWith(true);
    expect(active).toHaveBeenLastCalledWith(false);
    expect(drop).toHaveBeenCalledWith(ref);
    Reflect.deleteProperty(document, "elementFromPoint");
    detach();
    target.remove();
  });
});
