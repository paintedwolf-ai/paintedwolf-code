"""Hosted verification workflow commands."""
import argparse
import json
import os
from pathlib import Path

from ci_policy import hosted_plan, hosted_execution, verification_report, verification_verdict


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    plan = commands.add_parser("matrix")
    plan.add_argument("profile", choices=sorted(hosted_plan.PROFILES))
    plan.add_argument("--affected", action="store_true")
    plan.add_argument("--suite", default="all", choices=sorted(hosted_plan.SUITES))
    plan.add_argument("--platform", action="store_true", help="only the profile's platform lanes")
    parallel = commands.add_parser("max-parallel", help="jobs a profile's matrix runs at once")
    parallel.add_argument("profile", choices=sorted(hosted_plan.PROFILES))
    run = commands.add_parser("run")
    run.add_argument("lane")
    run.add_argument("--shard", default="")
    gate = commands.add_parser("gate")
    gate.add_argument("--skipped", type=lambda value: [name for name in value.split(",") if name], default=[],
                      help="comma-separated jobs this tier skips on purpose")
    gate.add_argument("--draft", choices=["true", "false"], default="false",
                      help="whether the run verifies a draft pull request")
    summary = commands.add_parser("report")
    summary.add_argument("status")
    verified = commands.add_parser("verified")
    verified.add_argument("sha")
    release = commands.add_parser("release")
    release.add_argument("step", help="the step whose leftover requests are withdrawn")
    args = parser.parse_args()
    if args.command == "matrix":
        from ci_policy.impact import change
        scope = change() if args.affected else None
        if scope and os.environ.get("GITHUB_STEP_SUMMARY"):
            with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
                summary.write("### Change scope\n\n```json\n" + json.dumps(scope, indent=2) + "\n```\n")
        print(json.dumps(hosted_plan.matrix(args.profile, args.suite, scope, args.platform), separators=(",", ":")))
    elif args.command == "max-parallel":
        print(hosted_plan.max_parallel(args.profile))
    elif args.command == "run":
        return hosted_execution.run_lane(args.lane, args.shard)
    elif args.command == "gate":
        verification_verdict.require_success(json.loads(os.environ["NEEDS_JSON"]), args.skipped, args.draft == "true")
    elif args.command == "verified":
        verification_verdict.require_qualification(os.environ["GITHUB_REPOSITORY"], args.sha)
    elif args.command == "release":
        # Cleanup still runs; a request that would not release is reported, not fatal.
        unreleased = hosted_execution.release_leftover_requests(args.step)
        if unreleased:
            print(f"::warning::queue requests still held after cancellation: {', '.join(unreleased)}", flush=True)
    else:
        verification_report.report(args.status)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
