import type { SourceStepBriefingTarget } from "../../api/types.ts";

/**
 * Collects the host's explanation targets across Walk pages. A group listed
 * differently by two pages crosses a page boundary, so it has no target
 * rather than a union of partial ones.
 */
export class WalkBriefingTargets {
  private readonly targets = new Map<string, SourceStepBriefingTarget>();
  private readonly split = new Set<string>();

  add(page: Record<string, SourceStepBriefingTarget> | undefined): void {
    for (const [key, target] of Object.entries(page ?? {})) {
      if (this.split.has(key)) continue;
      const held = this.targets.get(key);
      if (held && held.scope_fingerprint !== target.scope_fingerprint) {
        this.targets.delete(key);
        this.split.add(key);
        continue;
      }
      this.targets.set(key, target);
    }
  }

  resolved(): ReadonlyMap<string, SourceStepBriefingTarget> {
    return new Map(this.targets);
  }
}
