# Ranked status report
rank(records) in ranking.py sorts by descending integer score, then ascending case-sensitive name, retaining exact ties and leaving its input unchanged. display_status in labels.py maps pending, active and done to Waiting, Running and Complete; other values map to Unknown.

Worker assignments: Ranking fix writes ranking.py. Status labels writes labels.py. Report review is a separate read-only worker scoped to both ranking.py and labels.py, reviewing their integrated versions. Cite its findings about both return expressions in the handoff.
Keep the supplied tests and this README unchanged. Use Python standard library only. Run the supplied checks with `python3 -B -m unittest discover`.
