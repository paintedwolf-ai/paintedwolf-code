"""Deterministic seeded generators and hand-written edge specs for CF05 (F01–F06)."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from ..change_runner import CHANGE_OP_KINDS, ChangeOp, ChangeScript, generate_change_script
from ..generators import TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng

CF05_LEDGER_SIDECAR_COUNTS: dict[str, int] = {
    "F01": 200,
    "F02": 20,
    "F03": 60,
    "F04": 40,
    "F05": 100,
    "F06": 40,
}

CF05_LEDGER_INPROCESS_COUNTS: dict[str, int] = {
    "F01": 2000,
    "F02": 0,
    "F03": 300,
    "F04": 200,
    "F05": 0,
    "F06": 400,
}

CF05_LEDGER_EDGE_COUNTS: dict[str, int] = {
    "F01": 6,
    "F02": 4,
    "F03": 2,
    "F04": 3,
    "F05": 5,
    "F06": 3,
}


@dataclass(frozen=True)
class F01Case:
    case_id: str
    selector: ReplaySelector
    tree_spec: TreeSpec
    change_script: ChangeScript
    is_edge: bool = False
    description: str = ""


@dataclass(frozen=True)
class F02Case:
    case_id: str
    selector: ReplaySelector
    storm_kind: str  # mass_create | mass_rename | mass_delete | branch_rewrite | overflow_fanout
    initial_files: int
    write_count: int
    is_edge: bool = False
    description: str = ""


@dataclass(frozen=True)
class F03Case:
    case_id: str
    selector: ReplaySelector
    tree_spec: TreeSpec
    change_script: ChangeScript
    open_tab_path: str
    is_edge: bool = False
    description: str = ""


@dataclass(frozen=True)
class F04Case:
    case_id: str
    selector: ReplaySelector
    route: str  # create | write | rename | copy | delete | write_then_outside | rename_no_dup | copy_then_delete
    is_edge: bool = False
    description: str = ""


@dataclass(frozen=True)
class F05Case:
    case_id: str
    selector: ReplaySelector
    movements: tuple[str, ...]  # checkout | commit | empty_commit | rebase | stash_cycle | pull | reset_hard
    is_edge: bool = False
    description: str = ""


@dataclass(frozen=True)
class F06Case:
    case_id: str
    selector: ReplaySelector
    client_count: int
    drop_count: int
    burst_writes: int
    test_stale_cursor: bool = False
    test_hidden_surface: bool = False
    is_edge: bool = False
    description: str = ""


def _clean_tree_options(entry_count: int, max_depth: int = 4) -> TreeGenOptions:
    return TreeGenOptions(
        entry_count=entry_count,
        max_depth=max_depth,
        include_hidden=False,
        include_ignored=False,
        include_empty_dirs=False,
        include_vcs=False,
        include_boundaries=False,
        include_nested_checkouts=False,
        include_paintedwolf=False,
        include_symlinks=False,
        include_binary=False,
        include_mixed_line_endings=False,
    )


def generate_f01_case(base_seed: str, idx: int) -> F01Case:
    """Generate one F01 change differential instance (1–200 writes over 1–500 entry tree)."""
    rng = seed_rng(base_seed, "F01:params", idx)
    # Cover the full 1..500 entry range and 1..200 write range across 200 instances
    if idx == 0:
        entry_count, op_count = 1, 1
    elif idx == 1:
        entry_count, op_count = 500, 200
    else:
        entry_count = rng.randint(2, 120) if idx % 10 != 0 else rng.randint(150, 500)
        op_count = rng.randint(1, 25) if idx % 10 != 0 else rng.randint(40, 200)
    tree_spec = generate_tree_spec(base_seed, "F01", idx, _clean_tree_options(entry_count, max_depth=4))
    script = generate_change_script(
        base_seed,
        "F01",
        idx,
        tree_spec.file_paths(),
        op_count=op_count,
        allowed_ops=CHANGE_OP_KINDS,
    )
    return F01Case(
        case_id=f"F01-{idx:04d}",
        selector=ReplaySelector(base_seed, "F01", idx),
        tree_spec=tree_spec,
        change_script=script,
    )


def f01_edge_cases(base_seed: str = BASE_SEED_PRIMARY) -> list[F01Case]:
    """6 hand-written F01 edge slots."""
    base_tree = TreeSpec(
        selector=ReplaySelector(base_seed, "F01_EDGE", 0),
        entries=(
            TreeEntrySpec("src/alpha.txt", "file", b"alpha initial\n"),
            TreeEntrySpec("src/beta.txt", "file", b"beta initial\n"),
            TreeEntrySpec(".hidden_cfg.txt", "file", b"hidden initial\n"),
            TreeEntrySpec(".paintedwolf/note.md", "file", b"pw note initial\n"),
        ),
    )
    edges = [
        F01Case(
            case_id="F01-EDGE-01",
            selector=ReplaySelector(base_seed, "F01_EDGE", 1),
            tree_spec=base_tree,
            change_script=ChangeScript(
                selector=ReplaySelector(base_seed, "F01_EDGE", 1),
                ops=(
                    ChangeOp("create", "src/ephemeral.txt", content=b"v1\n"),
                    ChangeOp("edit", "src/ephemeral.txt", content=b"v2\n"),
                    ChangeOp("delete", "src/ephemeral.txt"),
                ),
            ),
            is_edge=True,
            description="Create, edit, and delete same path in one burst",
        ),
        F01Case(
            case_id="F01-EDGE-02",
            selector=ReplaySelector(base_seed, "F01_EDGE", 2),
            tree_spec=base_tree,
            change_script=ChangeScript(
                selector=ReplaySelector(base_seed, "F01_EDGE", 2),
                ops=(
                    ChangeOp("atomic_replace", "src/alpha.txt", content=b"atomic replacement payload\n"),
                ),
            ),
            is_edge=True,
            description="Atomic replace via temp sibling + os.replace",
        ),
        F01Case(
            case_id="F01-EDGE-03",
            selector=ReplaySelector(base_seed, "F01_EDGE", 3),
            tree_spec=base_tree,
            change_script=ChangeScript(
                selector=ReplaySelector(base_seed, "F01_EDGE", 3),
                ops=(
                    ChangeOp("rename", "src/alpha.txt", new_path="src/alpha_mid.txt"),
                    ChangeOp("rename", "src/alpha_mid.txt", new_path="src/alpha_final.txt"),
                ),
            ),
            is_edge=True,
            description="Chained rename a -> b -> c in one burst",
        ),
        F01Case(
            case_id="F01-EDGE-04",
            selector=ReplaySelector(base_seed, "F01_EDGE", 4),
            tree_spec=base_tree,
            change_script=ChangeScript(
                selector=ReplaySelector(base_seed, "F01_EDGE", 4),
                ops=(
                    ChangeOp("chmod", "src/beta.txt", mode=0o755),
                ),
            ),
            is_edge=True,
            description="Permission-only chmod change with no content mutation",
        ),
        F01Case(
            case_id="F01-EDGE-05",
            selector=ReplaySelector(base_seed, "F01_EDGE", 5),
            tree_spec=base_tree,
            change_script=ChangeScript(
                selector=ReplaySelector(base_seed, "F01_EDGE", 5),
                ops=(
                    ChangeOp("edit", ".hidden_cfg.txt", content=b"hidden updated\n"),
                    ChangeOp("edit", ".paintedwolf/note.md", content=b"pw note updated\n"),
                ),
            ),
            is_edge=True,
            description="Hidden file and .paintedwolf/ file edits",
        ),
        F01Case(
            case_id="F01-EDGE-06",
            selector=ReplaySelector(base_seed, "F01_EDGE", 6),
            tree_spec=base_tree,
            change_script=ChangeScript(
                selector=ReplaySelector(base_seed, "F01_EDGE", 6),
                ops=(
                    ChangeOp("create", "nested/a/b/c/leaf.txt", content=b"deep leaf\n"),
                    ChangeOp("edit", "nested/a/b/c/leaf.txt", content=b"deep leaf v2\n"),
                    ChangeOp("delete", "nested/a/b/c/leaf.txt"),
                ),
            ),
            is_edge=True,
            description="Deep nested create/edit/delete leaving empty parent folders",
        ),
    ]
    assert len(edges) == CF05_LEDGER_EDGE_COUNTS["F01"]
    return edges


def generate_f02_case(base_seed: str, idx: int) -> F02Case:
    """Generate one F02 storm/overflow instance (2,000–50,000 writes)."""
    rng = seed_rng(base_seed, "F02:params", idx)
    kinds = ("mass_create", "mass_rename", "mass_delete", "branch_rewrite", "overflow_fanout")
    kind = kinds[idx % len(kinds)]
    # Cover full ledger range 2,000..50,000 writes across 20 instances
    if idx == 0:
        write_count = 2000
    elif idx == 19:
        write_count = 50000
    else:
        # Spread from 2000 to 12000 for most, with a couple of 20k-35k heavy cases
        if idx in (10, 15):
            write_count = rng.randint(20000, 35000)
        else:
            write_count = rng.randint(2000, 8000)
    initial_files = write_count if kind in ("mass_rename", "mass_delete", "branch_rewrite") else 200
    return F02Case(
        case_id=f"F02-{idx:04d}",
        selector=ReplaySelector(base_seed, "F02", idx),
        storm_kind=kind,
        initial_files=initial_files,
        write_count=write_count,
    )


def f02_edge_cases(base_seed: str = BASE_SEED_PRIMARY) -> list[F02Case]:
    """4 hand-written F02 edge slots."""
    edges = [
        F02Case(
            case_id="F02-EDGE-01",
            selector=ReplaySelector(base_seed, "F02_EDGE", 1),
            storm_kind="mass_rename",
            initial_files=2500,
            write_count=2500,
            is_edge=True,
            description="Mass rename of 2,500 files at once forcing resync + reconciliation",
        ),
        F02Case(
            case_id="F02-EDGE-02",
            selector=ReplaySelector(base_seed, "F02_EDGE", 2),
            storm_kind="mass_delete",
            initial_files=2500,
            write_count=2500,
            is_edge=True,
            description="Mass delete of 2,500 files at once forcing resync + reconciliation",
        ),
        F02Case(
            case_id="F02-EDGE-03",
            selector=ReplaySelector(base_seed, "F02_EDGE", 3),
            storm_kind="branch_rewrite",
            initial_files=2200,
            write_count=2200,
            is_edge=True,
            description="Git branch switch rewriting 2,200 files across tree",
        ),
        F02Case(
            case_id="F02-EDGE-04",
            selector=ReplaySelector(base_seed, "F02_EDGE", 4),
            storm_kind="overflow_fanout",
            initial_files=200,
            write_count=3000,
            is_edge=True,
            description="Deep directory fan-out >512 paths forcing watcher overflow resync",
        ),
    ]
    assert len(edges) == CF05_LEDGER_EDGE_COUNTS["F02"]
    return edges


def generate_f03_case(base_seed: str, idx: int) -> F03Case:
    """Generate one F03 observation-before-walk instance (1–50 writes during first walk)."""
    rng = seed_rng(base_seed, "F03:params", idx)
    entry_count = rng.randint(80, 250)
    op_count = 1 if idx == 0 else (50 if idx == 59 else rng.randint(1, 50))
    tree_spec = generate_tree_spec(base_seed, "F03", idx, _clean_tree_options(entry_count, max_depth=4))
    files = tree_spec.file_paths()
    open_tab = files[0] if files else "src/open_tab.txt"
    other_files = [f for f in files if f != open_tab]
    # Always include an edit to the open tab plus generated ops up to op_count (without deleting open_tab)
    extra_ops = generate_change_script(
        base_seed,
        "F03",
        idx,
        other_files,
        op_count=max(0, op_count - 1),
        allowed_ops=("create", "edit", "delete"),
    ).ops
    all_ops = (
        ChangeOp("edit", open_tab, content=f"outside edit during first walk idx={idx}\n".encode("utf-8")),
    ) + extra_ops
    return F03Case(
        case_id=f"F03-{idx:04d}",
        selector=ReplaySelector(base_seed, "F03", idx),
        tree_spec=tree_spec,
        change_script=ChangeScript(ReplaySelector(base_seed, "F03", idx), all_ops),
        open_tab_path=open_tab,
    )


def f03_edge_cases(base_seed: str = BASE_SEED_PRIMARY) -> list[F03Case]:
    """2 hand-written F03 edge slots."""
    tree = generate_tree_spec(base_seed, "F03_EDGE", 0, _clean_tree_options(150, max_depth=4))
    files = tree.file_paths()
    target = files[0]
    edges = [
        F03Case(
            case_id="F03-EDGE-01",
            selector=ReplaySelector(base_seed, "F03_EDGE", 1),
            tree_spec=tree,
            change_script=ChangeScript(
                ReplaySelector(base_seed, "F03_EDGE", 1),
                ops=(ChangeOp("edit", target, content=b"edge01 open tab edit during first walk\n"),),
            ),
            open_tab_path=target,
            is_edge=True,
            description="Single edit to open editor tab while first catalog walk is running",
        ),
        F03Case(
            case_id="F03-EDGE-02",
            selector=ReplaySelector(base_seed, "F03_EDGE", 2),
            tree_spec=tree,
            change_script=ChangeScript(
                ReplaySelector(base_seed, "F03_EDGE", 2),
                ops=(
                    ChangeOp("create", "during_walk/new_file.txt", content=b"created mid-walk\n"),
                    ChangeOp("edit", target, content=b"edited mid-walk\n"),
                    ChangeOp("delete", files[-1]),
                ),
            ),
            open_tab_path=target,
            is_edge=True,
            description="Create + edit open tab + delete while first walk is running",
        ),
    ]
    assert len(edges) == CF05_LEDGER_EDGE_COUNTS["F03"]
    return edges


def generate_f04_case(base_seed: str, idx: int) -> F04Case:
    """Generate one F04 host write route instance."""
    routes = ("create", "write", "rename", "copy", "delete")
    route = routes[idx % len(routes)]
    return F04Case(
        case_id=f"F04-{idx:04d}",
        selector=ReplaySelector(base_seed, "F04", idx),
        route=route,
    )


def f04_edge_cases(base_seed: str = BASE_SEED_PRIMARY) -> list[F04Case]:
    """3 hand-written F04 edge slots."""
    edges = [
        F04Case(
            case_id="F04-EDGE-01",
            selector=ReplaySelector(base_seed, "F04_EDGE", 1),
            route="write_then_outside",
            is_edge=True,
            description="Host write followed immediately by distinct outside edit (bytes differ, not suppressed by time)",
        ),
        F04Case(
            case_id="F04-EDGE-02",
            selector=ReplaySelector(base_seed, "F04_EDGE", 2),
            route="rename_no_dup",
            is_edge=True,
            description="Host rename emits exactly one rename event and one effect, no duplicate watcher echo",
        ),
        F04Case(
            case_id="F04-EDGE-03",
            selector=ReplaySelector(base_seed, "F04_EDGE", 3),
            route="copy_then_delete",
            is_edge=True,
            description="Host copy followed by host delete produces exact 1 effect per host write",
        ),
    ]
    assert len(edges) == CF05_LEDGER_EDGE_COUNTS["F04"]
    return edges


def generate_f05_case(base_seed: str, idx: int) -> F05Case:
    """Generate one F05 Git movement script (1–10 movements)."""
    rng = seed_rng(base_seed, "F05:params", idx)
    all_moves = ("checkout", "commit", "empty_commit", "rebase", "stash_cycle", "pull", "reset_hard")
    if idx == 0:
        count = 1
    elif idx == 99:
        count = 10
    else:
        count = rng.randint(1, 10)
    moves = tuple(rng.choice(all_moves) for _ in range(count))
    return F05Case(
        case_id=f"F05-{idx:04d}",
        selector=ReplaySelector(base_seed, "F05", idx),
        movements=moves,
    )


def f05_edge_cases(base_seed: str = BASE_SEED_PRIMARY) -> list[F05Case]:
    """5 hand-written F05 edge slots."""
    edges = [
        F05Case(
            case_id="F05-EDGE-01",
            selector=ReplaySelector(base_seed, "F05_EDGE", 1),
            movements=("checkout",),
            is_edge=True,
            description="Git branch checkout modifying/adding/deleting tracked files",
        ),
        F05Case(
            case_id="F05-EDGE-02",
            selector=ReplaySelector(base_seed, "F05_EDGE", 2),
            movements=("rebase",),
            is_edge=True,
            description="Git rebase replaying branch commits onto updated main",
        ),
        F05Case(
            case_id="F05-EDGE-03",
            selector=ReplaySelector(base_seed, "F05_EDGE", 3),
            movements=("stash_cycle",),
            is_edge=True,
            description="Git stash push and stash pop restoring working tree changes",
        ),
        F05Case(
            case_id="F05-EDGE-04",
            selector=ReplaySelector(base_seed, "F05_EDGE", 4),
            movements=("pull",),
            is_edge=True,
            description="Git pull fast-forward from local bare remote",
        ),
        F05Case(
            case_id="F05-EDGE-05",
            selector=ReplaySelector(base_seed, "F05_EDGE", 5),
            movements=("reset_hard", "empty_commit"),
            is_edge=True,
            description="Git reset --hard HEAD~1 followed by empty commit (git_changed with zero file changes)",
        ),
    ]
    assert len(edges) == CF05_LEDGER_EDGE_COUNTS["F05"]
    return edges


def generate_f06_case(base_seed: str, idx: int) -> F06Case:
    """Generate one F06 subscriber script (1–6 clients, 1–20 drops)."""
    rng = seed_rng(base_seed, "F06:params", idx)
    if idx == 0:
        clients, drops = 1, 1
    elif idx == 39:
        clients, drops = 6, 20
    else:
        clients = rng.randint(1, 6)
        drops = rng.randint(1, 20)
    burst = rng.randint(2, 6)
    return F06Case(
        case_id=f"F06-{idx:04d}",
        selector=ReplaySelector(base_seed, "F06", idx),
        client_count=clients,
        drop_count=drops,
        burst_writes=burst,
        test_stale_cursor=(idx % 4 == 0),
        test_hidden_surface=(idx % 3 == 0),
    )


def f06_edge_cases(base_seed: str = BASE_SEED_PRIMARY) -> list[F06Case]:
    """3 hand-written F06 edge slots."""
    edges = [
        F06Case(
            case_id="F06-EDGE-01",
            selector=ReplaySelector(base_seed, "F06_EDGE", 1),
            client_count=2,
            drop_count=1,
            burst_writes=3,
            test_stale_cursor=True,
            is_edge=True,
            description="Invalid/stale cursor triggers HTTP 409 event_replay_unavailable with fresh event_cursor",
        ),
        F06Case(
            case_id="F06-EDGE-02",
            selector=ReplaySelector(base_seed, "F06_EDGE", 2),
            client_count=3,
            drop_count=2,
            burst_writes=4,
            is_edge=True,
            description="Mid-burst drop of 1 of 3 clients; survivors receive burst, dropped client reconnects",
        ),
        F06Case(
            case_id="F06-EDGE-03",
            selector=ReplaySelector(base_seed, "F06_EDGE", 3),
            client_count=2,
            drop_count=1,
            burst_writes=3,
            test_hidden_surface=True,
            is_edge=True,
            description="Hidden surface unsubscribes, receives 0 wakes during burst, revalidates on return",
        ),
    ]
    assert len(edges) == CF05_LEDGER_EDGE_COUNTS["F06"]
    return edges
