def format_report(records):
    lines = ["Service          Score", "---------------- -----"]
    lines.extend(f"{row['name']:<16} {row['score']:>5}" for row in records)
    lines.append(f"Total: {len(records)}")
    return "\n".join(lines)
