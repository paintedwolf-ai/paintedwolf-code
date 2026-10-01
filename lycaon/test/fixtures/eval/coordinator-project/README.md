# Dispatch planner

A dependency-free Python CLI for a local batch planner. Run `python3 app.py jobs.json` and `python3 -m unittest`.

Job fields: id (nonempty string), priority (integer), depends_on (array of IDs). The current prototype prints input IDs. Jobs execute in sequential stages, with jobs within a stage eligible to run concurrently.
