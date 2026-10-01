def collect_lines(text):
    """Collect nonblank lines from an independent plain-text input."""
    return [line.strip() for line in text.splitlines() if line.strip()]
