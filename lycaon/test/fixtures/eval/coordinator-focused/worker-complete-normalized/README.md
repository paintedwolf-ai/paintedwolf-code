# Status labels
display_status accepts any string. Strip whitespace and casefold it; pending, active and done map to Waiting, Running and Complete. All other strings map to Unknown. Preserve settings.json.
Keep the supplied tests and this README unchanged. Use regular project files, Python standard library only, and no downloads or dependencies.
Run the full supplied suite with `python3 -B -m unittest discover`.

Keep the existing checklist labels unchanged.
