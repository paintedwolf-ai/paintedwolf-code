// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { APP_SCOPE, noticeScopeKey } from "../../notices/notice-scope.ts";
import { createUpdateState } from "./update-state.ts";
import { mountUpdateNotice } from "./mount-update-notice.ts";
import { packageFixture, updateFixture, stagedFixture, updateServiceFixture } from "./update-test-fixture.ts";
import type { NativeUpdateState } from "./update-service.ts";

async function fixture() {
  let deliver!: (state: NativeUpdateState) => void;
  const notices = createNoticeStore();
  const updates = createUpdateState(updateServiceFixture({ subscribe: async (observe) => { deliver = observe; return () => {}; } }));
  const stop = mountUpdateNotice({ notices, updates });
  await vi.waitFor(() => expect(updates.state()).not.toBeNull());
  let revision = 2;
  return { stop, notices, rows: () => notices.index().get(noticeScopeKey(APP_SCOPE)) ?? [], send: async (state: NativeUpdateState) => {
    deliver({ ...state, revision: revision++ });
    await Promise.resolve();
  } };
}

describe("update notices", () => {
  it("keeps unavailable development builds quiet", async () => {
    const f = await fixture();
    try {
      const state = updateFixture({ last_error: { code: "feed_rejected" } });
      state.capabilities.blocked_reason = "development_build";
      await f.send(state);
      expect(f.rows()).toHaveLength(0);
    } finally { f.stop(); }
  });
  it("announces readiness once, respects dismissal, and withdraws obsolete offers", async () => {
    const f = await fixture();
    try {
      expect(f.rows()).toHaveLength(0);
      await f.send(stagedFixture());
      expect(f.rows().map(row => row.code)).toEqual(["update_ready"]);
      f.notices.dismiss(f.rows()[0]!.id);
      await f.send(stagedFixture());
      expect(f.rows()).toHaveLength(0);
      await f.send(updateFixture({ candidate: null, discovery: "up_to_date" }));
      await f.send(stagedFixture());
      expect(f.rows()).toHaveLength(1);
      await f.send(updateFixture({ candidate: null }));
      expect(f.rows()).toHaveLength(0);
    } finally { f.stop(); }
  });
  it("withdraws readiness after a rejected feed and restores it after a valid check", async () => {
    const f = await fixture();
    try {
      await f.send(stagedFixture());
      await f.send(stagedFixture({ offer_confirmed_at: null, last_error: { code: "feed_rejected" } }));
      expect(f.rows().map(row => row.code)).toEqual(["update_feed_rejected"]);
      expect(f.rows()[0]!.message).toContain("No update was installed");
      await f.send(stagedFixture({ offer_confirmed_at: null, last_error: { code: "check_failed" } }));
      expect(f.rows()).toHaveLength(0);
      await f.send(stagedFixture());
      expect(f.rows().map(row => row.code)).toEqual(["update_ready"]);
    } finally { f.stop(); }
  });
  it("announces a newer package-managed release even though nothing can be downloaded", async () => {
    const f = await fixture();
    try {
      await f.send(packageFixture());
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
      await f.send(packageFixture({ automatic_updates_enabled: false }));
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
      const brew = updateFixture({ install_source: "homebrew_cask", capabilities: { can_check: true, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "package_managed" } });
      await f.send(brew);
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
      await f.send({ ...brew, automatic_updates_enabled: false });
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
      const held = packageFixture();
      held.candidate!.rollout_eligibility = "held_back";
      await f.send(held);
      expect(f.rows()).toHaveLength(0);
      await f.send({ ...brew, candidate: { ...brew.candidate!, rollout_eligibility: "held_back" } });
      expect(f.rows()).toHaveLength(0);
      await f.send(packageFixture({ candidate: null, discovery: "up_to_date" }));
      expect(f.rows()).toHaveLength(0);
      // Automatic updates on a downloadable install stay quiet: the host installs on quit.
      await f.send(updateFixture());
      expect(f.rows()).toHaveLength(0);
    } finally { f.stop(); }
  });
  it("keeps held-back and offline checks quiet and uses the existing notice rail for failures", async () => {
    const f = await fixture();
    try {
      const held = updateFixture();
      held.candidate!.rollout_eligibility = "held_back";
      await f.send({ ...held, automatic_updates_enabled: false, last_error: { code: "check_failed" } });
      expect(f.rows()).toHaveLength(0);
      await f.send(updateFixture({ installation: "failed", last_error: { code: "install_failed" } }));
      expect(f.rows().map(row => row.code)).toEqual(["update_failed"]);
      await f.send(updateFixture({ automatic_updates_enabled: false }));
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
    } finally { f.stop(); }
  });
});
