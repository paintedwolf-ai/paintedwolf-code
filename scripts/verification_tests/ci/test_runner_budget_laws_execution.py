"""The plan's hosted ceilings bound every job of a scheduled run that can hold a runner at once,
through each reusable workflow it calls."""
from itertools import product
import json
import unittest

from ci_policy import hosted_plan
from release_preparation import plan as preparation_plan
from verification_tests.ci.workflows import ROOT, plan, render, workflow


def is_macos(runner):
    return str(runner).startswith("macos")


def expanded(matrix):
    """The rows of a static matrix: the product of its axes, then its includes."""
    axes = {key: values for key, values in matrix.items() if key not in {"include", "exclude"}}
    rows = [dict(zip(axes, values)) for values in product(*axes.values())] if axes else []
    return rows + list(matrix.get("include", []))


def lane_runners(profile):
    return [lane.get("runner", "ubuntu-24.04") for lane in hosted_plan.lanes().values()
            if profile in lane["profiles"] for _ in range(lane.get("shards", 1))]


def prepared_runners(kind):
    catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())
    return [row["runner"] for row in preparation_plan(catalog)[kind]["include"]]


# Matrices whose rows a job computes at run time, by the catalog that computes them.
DYNAMIC = {
    ("verification", "verify"): lambda inputs: lane_runners(inputs["profile"]),
    ("release-prepare", "linux-packages"): lambda inputs: prepared_runners("linux"),
    ("release-prepare", "nix-closures"): lambda inputs: prepared_runners("nix"),
    ("release-prepare", "macos-app"): lambda inputs: prepared_runners("macos"),
}


def weight(name, job, inputs):
    """Runners a job holds at once, and how many of them are macOS."""
    runner = str(job.raw.get("runs-on", ""))
    strategy = job.raw.get("strategy") or {}
    if "matrix" not in strategy:
        return 1, int(is_macos(runner))
    limit = strategy.get("max-parallel")
    if (name, job.name) == ("verification", "verify"):
        limit = hosted_plan.max_parallel(inputs["profile"])
    elif limit is not None:
        limit = int(render(limit, {"inputs": inputs}))
    if (name, job.name) in DYNAMIC:
        runners = DYNAMIC[name, job.name](inputs)
    elif isinstance(strategy["matrix"], dict):
        runners = [render(runner, {"matrix": row, "inputs": inputs}) for row in expanded(strategy["matrix"])]
    elif limit is None:
        raise AssertionError(f"{name} {job.name}: a matrix computed at run time needs a max-parallel")
    else:
        # Rows unknown until the run: every runner it may hold counts, as macOS unless it names another.
        return limit, limit if is_macos(runner) or "matrix." in runner else 0
    held = min(len(runners), limit) if limit else len(runners)
    return held, min(held, sum(map(is_macos, runners)))


def flatten(name, inputs, prefix=""):
    """Every job of a run, with its weight and the jobs that finish before it starts."""
    spec = workflow(name)
    nodes, members = {}, {}
    for job_name, job in spec.jobs.items():
        key = prefix + job_name
        called = job.called()
        if called:
            values = {field: value for field, value in (job.raw.get("with") or {}).items() if "${{" not in str(value)}
            child = flatten(called.path.removeprefix(".github/workflows/").removesuffix(".yml"), values, key + "/")
            nodes.update(child)
            members[job_name] = set(child)
        else:
            nodes[key] = {"weight": weight(name, job, inputs), "after": set()}
            members[job_name] = {key}
    for job_name, job in spec.jobs.items():
        for node in members[job_name]:
            nodes[node]["after"] |= set().union(*(members[need] for need in job.needs))
    return nodes


def peak(nodes, index):
    """The heaviest set of jobs no `needs` chain orders, by one weight."""
    before = {}

    def ancestors(node):
        if node not in before:
            before[node] = set(nodes[node]["after"]).union(*(ancestors(parent) for parent in nodes[node]["after"]))
        return before[node]

    order = sorted(nodes)
    comparable = {node: ancestors(node) | {other for other in nodes if node in ancestors(other)} for node in order}
    best = 0

    def search(position, chosen, total):
        nonlocal best
        best = max(best, total)
        for at in range(position, len(order)):
            node = order[at]
            if nodes[node]["weight"][index] and not comparable[node] & chosen:
                search(at + 1, chosen | {node}, total + nodes[node]["weight"][index])

    search(0, frozenset(), 0)
    return best


class RunnerBudgetLaws(unittest.TestCase):
    def test_each_scheduled_run_fits_the_plans_hosted_ceilings(self):
        ceilings = plan()["capacity"]["hosted"]
        self.assertLessEqual(ceilings["macos"], ceilings["jobs"])
        for name, inputs in (("nightly", {}), ("qualification", {})):
            with self.subTest(run=name):
                nodes = flatten(name, inputs)
                self.assertLessEqual(peak(nodes, 0), ceilings["jobs"])
                self.assertLessEqual(peak(nodes, 1), ceilings["macos"])

    def test_the_peak_counts_jobs_that_overlap_and_not_jobs_a_needs_chain_orders(self):
        node = lambda weight, *after: {"weight": weight, "after": set(after)}
        nodes = {"a": node((6, 2)), "b": node((3, 1), "a"), "c": node((4, 1)), "d": node((1, 1), "c")}
        self.assertEqual((peak(nodes, 0), peak(nodes, 1)), (10, 3))

    def test_a_matrix_is_weighed_by_its_rows_and_its_parallel_limit(self):
        spec = workflow("e2e-verification")
        self.assertEqual(weight("e2e-verification", spec.jobs["playwright-web"], {"web-parallel": 1}), (1, 0))
        self.assertEqual(weight("e2e-verification", spec.jobs["playwright-web"], {"web-parallel": 5}), (2, 0))
        self.assertEqual(weight("e2e-verification", spec.jobs["playwright-desktop"], {}), (1, 1))
        verify = weight("verification", workflow("verification").jobs["verify"], {"profile": "nightly"})
        self.assertEqual(verify[0], hosted_plan.max_parallel("nightly"))
        self.assertEqual(verify[1], sum(map(is_macos, lane_runners("nightly"))))


if __name__ == "__main__":
    unittest.main()
