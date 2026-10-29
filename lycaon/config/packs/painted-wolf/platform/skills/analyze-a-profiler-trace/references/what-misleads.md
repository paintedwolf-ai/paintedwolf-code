# What misleads people in trace analysis

- **The biggest number in the file is usually a container.** A recording whose frame records total 99.6% of its own wall clock is not 99.6% busy; that instrument emits one record per frame and they tile the timeline by construction.
- **Sample timestamps are often unusable.** Exporters write zero for most of them — 824 of 841 in one real recording — with only the tail carrying real values. Self and total time are order-independent aggregates and survive this; anything that places samples on the timeline does not.
- **Total time is not blame.** A framework's scheduler holding 54% of total time is the pipe every update flows through, not the cost. Only self time nominates code.
- **A freshly exported file may still be mid-write.** A first parse that returns a fraction of the expected records is a truncated read, not a short recording. Re-parse and compare counts.
- **Idle sampled as work.** V8 profiles carry `(idle)`, `(program)`, and `(garbage collector)` pseudo-frames; leaving `(idle)` in makes it the top entry of every profile ever taken.
- **A profiler running changes what it measures.** Sampling perturbs less than instrumentation, but a dev build, an open inspector, and a sourcemapped bundle are all slower than what ships.
- **One recording is one sample.** A hot path that appears in one capture of one interaction is a hypothesis until a second capture reproduces it.
