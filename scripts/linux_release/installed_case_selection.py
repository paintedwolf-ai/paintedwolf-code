"""Select bounded declared cases without turning partial execution into qualification."""
from __future__ import annotations


def selected_cases(value, rows):
    if value is None:
        return None
    if (not isinstance(value, dict) or set(value) != {'format_version', 'cases'}
            or type(value['format_version']) is not int or value['format_version'] != 1
            or not isinstance(value['cases'], list) or not 1 <= len(value['cases']) <= 64):
        raise ValueError('case selection requires version1 and one to64 declared cases')
    declared = {(row['capability'], case) for row in rows for case in row['cases']}
    selected = set()
    for item in value['cases']:
        if (not isinstance(item, dict) or set(item) != {'capability', 'case'}
                or any(not isinstance(item[key], str) for key in item)):
            raise ValueError('case selection requires exact capability and case identifiers')
        key = item['capability'], item['case']
        if key not in declared or key in selected:
            raise ValueError('case selection repeats a case or selects no applicable catalog case')
        selected.add(key)
    return frozenset(selected)
