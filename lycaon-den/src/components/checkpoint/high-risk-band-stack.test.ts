import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/** High-risk emphasis stays within the approval card. */
const denSrc = join(import.meta.dirname, "../..");

describe("high-risk band notification-stack containment", () => {
  it("Approval card path does not import notice/nudge/attention/notification modules", () => {
    const src = readFileSync(
      join(denSrc, "components/checkpoint/ApprovalCard.tsx"),
      "utf8",
    );
    const forbidden = [
      "notifications/",
      "notices/",
      "attention/",
      "SystemNudge",
      "CriticalStop",
      "NoticeRail",
      "createNotificationService",
    ];
    for (const needle of forbidden) {
      expect(src, `must not reference ${needle}`).not.toContain(needle);
    }
  });

  it("notification service still has exactly three classes", () => {
    const src = readFileSync(join(denSrc, "notifications/notification-service.ts"), "utf8");
    expect(src).toContain(
      'export type NotificationClass = "Finished" | "NeedsYou" | "NeedsApproval"',
    );
    expect(src).not.toMatch(/HighRisk|high_risk/);
  });
});
