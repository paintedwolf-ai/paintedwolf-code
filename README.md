# Fuzz findings

This branch stores unresolved crashers found by the nightly fuzz lane (`scripts/fuzz_plan/findings.py`).

- Each finding is stored at its repository path, `lycaon/<pkg>/testdata/fuzz/<Target>/<id>`, byte-identical to what `go test -fuzz` wrote. Next to it is `<id>.finding.json`, which records the package, target, id, main commit, run id, the failure output, and whether a single replay reproduced the failure.
- Every PR's `test:fuzz:findings` check, in both the ready and merge-queue profiles, fails until a fix PR commits the finding's input byte-identically into the package's `testdata/fuzz/<Target>/`.
- Once that input lands on `main`, `.github/workflows/clear-fuzz-findings.yml` removes both files from this branch.
