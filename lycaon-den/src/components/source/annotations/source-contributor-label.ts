import type { SourceContributor } from "../../../api/types.ts";
import { isCallerPerson } from "../../../platform/connection/host-identity.ts";

/** Names a person relative to the caller; the wire carries no display name. */
export function personLabel(personId: string): string {
  return isCallerPerson(personId) ? "You" : "Another person";
}

export function sourceContributorLabel(author: SourceContributor): string {
  switch (author.origin) {
    case "user":
      return personLabel(author.person_id ?? "");
    case "external":
      return "Outside app";
    case "agent":
      return `From ${author.actor_label || "an untitled chat"}${author.turn ? ` · turn ${author.turn}` : ""}`;
  }
}
