import { For, Show } from "solid-js";
import type { PackageSourceFacts } from "../../settings/system/update-service.ts";

const REASONS: Record<NonNullable<PackageSourceFacts["reason"]>, string> = {
  unsupported_owner: "The configuration or profile that owns this source is unknown.",
  adapter_unavailable: "The installed package-source query interface is unavailable.",
  not_installed: "The running package is no longer present in the package database.",
  no_cached_candidate: "No candidate from a configured repository is present in the local metadata.",
  policy_unobserved: "This query does not establish all holds, version locks or transaction policy. Update availability is unknown.",
  held: "Your package-manager configuration holds this package back.",
  untrusted_source: "The selected candidate includes a source the package manager does not trust.",
  ambiguous_candidate: "The package manager returned more than one candidate for this installation.",
  wrong_architecture: "The candidate architecture does not match this installation.",
  query_failed: "The package-source query failed. Earlier source results are no longer current.",
  timed_out: "The package-source query timed out.",
  output_limit: "The package-source query exceeded its output limit.",
  cancelled: "The package-source query was cancelled.",
  invalid_response: "The package-source query returned an unsupported response.",
};

export function PackageSourceStatus(props: { facts: PackageSourceFacts }) {
  return <Show when={props.facts.availability !== "not_checked"}>
    <div data-testid="updates-package-source">
      <Show when={props.facts.database_version}>{(version) => <p data-testid="updates-database-version">Package database version: {version()}{props.facts.architecture ? ` (${props.facts.architecture})` : ""}. The running version is shown separately above.</p>}</Show>
      <Show when={props.facts.candidate}>{(candidate) => <>
        <p data-testid="updates-source-candidate">Cached package candidate: {candidate().version} ({candidate().architecture}){candidate().newer_than_installed ? ". The package manager reports it as newer than the database version." : ". It is not newer than the database version."}</p>
        <For each={candidate().repositories}>{(repository) => <p>Source: {repository.id}{repository.suite ? `; suite: ${repository.suite}` : ""}. Its release channel is independent of your upstream feed preference.</p>}</For>
      </>}</Show>
      <Show when={props.facts.availability === "no_newer_candidate"}><p>No newer package was selected from the local repository metadata.</p></Show>
      <Show when={props.facts.reason}>{(reason) => <p data-testid="updates-source-reason">{REASONS[reason()]}</p>}</Show>
      <Show when={props.facts.cached_only}><p>Repository metadata was read from the local cache without refreshing it. Remote freshness and successful installation are not established. Review these results with your package manager before updating.</p></Show>
    </div>
  </Show>;
}
