// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { APP_SCOPE, noticeScopeKey } from "../../notices/notice-scope.ts";
import { createUpdateState } from "./update-state.ts";
import { mountUpdateNotice } from "./mount-update-notice.ts";
import { packageFixture, packageSourceFixture, updateFixture, stagedFixture, updateServiceFixture } from "./update-test-fixture.ts";
import type { NativeUpdateState, PackageSourceFacts } from "./update-service.ts";
// Each fixture is what the native adapter produces; the Rust source tests hold them equal.
import aptCandidate from "./fixtures/package-source-apt-candidate.json";
import aptHeld from "./fixtures/package-source-apt-held.json";
import pacmanCandidate from "./fixtures/package-source-pacman-candidate.json";
import pacmanIgnored from "./fixtures/package-source-pacman-ignored.json";
import dnfCandidate from "./fixtures/package-source-dnf-candidate.json";

const produced = (facts: unknown): PackageSourceFacts => facts as PackageSourceFacts;

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
  it("uses native source observations independently of upstream feed releases", async () => {
    const f = await fixture();
    const source = packageSourceFixture({ availability: "candidate_found", held: false,
      candidate: { version: "1.0.3-1", architecture: "amd64", repositories: [{ id: "configured", suite: "stable", trusted: true }], newer_than_installed: true } });
    const state = packageFixture({ candidate: null, discovery: "up_to_date" });
    state.package!.source = source;
    try {
      await f.send(packageFixture());
      expect(f.rows()).toHaveLength(0);
      const brew = updateFixture({ install_source: "homebrew_cask", capabilities: { can_check: true, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "package_managed" } });
      await f.send(brew);
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
      await f.send({ ...brew, automatic_updates_enabled: false });
      expect(f.rows().map(row => row.code)).toEqual(["update_available"]);
      await f.send({ ...brew, candidate: { ...brew.candidate!, rollout_eligibility: "held_back" } });
      expect(f.rows()).toHaveLength(0);
      await f.send(state);
      expect(f.rows().map(row => row.code)).toEqual(["package_update_listed"]);
      expect(f.rows()[0]!.message).toContain("does not confirm");
      f.notices.dismiss(f.rows()[0]!.id);
      await f.send({ ...state, candidate: updateFixture().candidate });
      expect(f.rows()).toHaveLength(0);
      const next = { ...source, candidate: { ...source.candidate!, version: "1.0.4-1" } };
      for (const inactive of [
        { ...next, availability: "blocked" as const, reason: "held" as const, held: true },
        { ...next, availability: "blocked" as const, reason: "untrusted_source" as const },
        { ...next, availability: "blocked" as const, reason: "owner_changed" as const, candidate: null },
        { ...next, availability: "unknown" as const, reason: "no_cached_candidate" as const, candidate: null },
        { ...next, availability: "unknown" as const, reason: "policy_unobserved" as const, held: null },
        { ...next, availability: "unknown" as const, reason: "unsupported_owner" as const, owner: null, candidate: null },
        { ...next, availability: "failed" as const, reason: "query_failed" as const, candidate: null },
        { ...next, availability: "failed" as const, reason: "timed_out" as const, candidate: null },
        { ...next, availability: "no_newer_candidate" as const, candidate: { ...next.candidate, newer_than_installed: false } },
        { ...next, availability: "not_checked" as const, candidate: null, held: null },
        { ...next, held: true },
        { ...next, held: null },
        { ...next, owner: null },
        { ...next, candidate: null },
        { ...next, candidate: { ...next.candidate, newer_than_installed: false } },
      ]) {
        state.package!.source = next;
        await f.send(state);
        expect(f.rows().map(row => row.code)).toEqual(["package_update_listed"]);
        state.package!.source = inactive;
        await f.send(state);
        expect(f.rows()).toHaveLength(0);
      }
      state.package!.source = next;
      await f.send({ ...state, discovery: "failed", last_error: { code: "check_failed" } });
      expect(f.rows().map(row => row.code)).toEqual(["package_update_listed"]);
      await f.send({ ...state, discovery: "failed", last_error: { code: "feed_rejected" } });
      expect(f.rows().map(row => row.code)).toEqual(["update_feed_rejected"]);
    } finally { f.stop(); }
  });
  it("announces the package candidates that APT and pacman report", async () => {
    for (const [family, facts] of [["deb", aptCandidate], ["pacman", pacmanCandidate]] as const) {
      const f = await fixture();
      const state = packageFixture({ candidate: null, discovery: "up_to_date" });
      state.package = { ...state.package!, family, source: produced(facts) };
      try {
        await f.send(state);
        expect(f.rows().map(row => row.code)).toEqual(["package_update_listed"]);
      } finally { f.stop(); }
    }
  });
  it("stays quiet for reported holds, ignore rules, unobserved versionlock policy and Nix builds", async () => {
    const nixSource = packageSourceFixture({ availability: "unknown", owner: null, reason: "unsupported_owner" });
    for (const [family, facts] of [["deb", aptHeld], ["pacman", pacmanIgnored], ["rpm", dnfCandidate], ["nix", nixSource]] as const) {
      const f = await fixture();
      const state = packageFixture({ candidate: updateFixture().candidate, discovery: "available" });
      state.package = { ...state.package!, family, source: produced(facts) };
      try {
        await f.send(state);
        expect(f.rows()).toHaveLength(0);
      } finally { f.stop(); }
    }
  });
  it("stays quiet while package ownership is unknown", async () => {
    const f = await fixture();
    const capabilities = { can_check: true, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "package_ownership_unknown" };
    try {
      for (const automatic_updates_enabled of [true, false]) {
        await f.send(updateFixture({ install_source: "package_ownership_unknown", capabilities, automatic_updates_enabled }));
        expect(f.rows()).toHaveLength(0);
      }
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
