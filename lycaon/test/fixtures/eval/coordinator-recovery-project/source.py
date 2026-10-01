import json


def load_profiles(path):
    with open(path, encoding="utf-8") as stream:
        records = json.load(stream)
    if not isinstance(records, list):
        raise ValueError("Expected a list of service records")
    for row in records:
        if not isinstance(row, dict) or not isinstance(row.get("name"), str):
            raise ValueError("Each record needs a service name")
        if type(row.get("score")) is not int or not 0 <= row["score"] <= 100:
            raise ValueError("Score must be an integer from 0 to 100")
    return records
