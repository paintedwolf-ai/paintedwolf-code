import { describe, expect, it, vi } from "vitest";
import { clearNotificationsOnRead } from "./clear-on-read.ts";

describe("clearNotificationsOnRead", () => {
  it("withdraws the chat's notifications after its seen stamp lands", async () => {
    const order: string[] = [];
    const mark = vi.fn(async (id: string) => {
      order.push(`mark:${id}`);
    });
    const cancel = vi.fn(async (ids: readonly string[]) => {
      order.push(`cancel:${ids.join(",")}`);
    });
    await clearNotificationsOnRead(mark, cancel)("chat-a");
    expect(order).toEqual(["mark:chat-a", "cancel:chat-a"]);
  });

  it("keeps notifications when the seen stamp fails", async () => {
    const cancel = vi.fn(async () => undefined);
    const read = clearNotificationsOnRead(async () => {
      throw new Error("offline");
    }, cancel);
    await expect(read("chat-a")).rejects.toThrow("offline");
    expect(cancel).not.toHaveBeenCalled();
  });
});
