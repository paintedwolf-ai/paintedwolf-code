"""Encode an AppArmor attachment that matches exactly one literal path."""


def literal(value: str) -> str:
    if not value or any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise ValueError("AppArmor attachment contains a control character")
    return '"' + "".join("\\" + char if char in '\\"*?[]{}^' else char for char in value) + '"'
