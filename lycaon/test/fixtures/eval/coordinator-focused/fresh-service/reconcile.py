def reconcile(events, minimum=0):
    seen, balances = {}, {}
    for event in events:
        key = (event["account"], event["cents"])
        if event["id"] in seen:
            if seen[event["id"]] != key:
                raise ValueError("conflicting duplicate")
            continue
        seen[event["id"]] = key
        balances[event["account"]] = balances.get(event["account"], 0) + event["cents"]
    return [{"account":account,"cents":cents} for account,cents in sorted(balances.items()) if cents >= minimum]
