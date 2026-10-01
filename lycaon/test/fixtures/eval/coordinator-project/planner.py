def build_plan(jobs):
    return {"stages": [[job["id"] for job in jobs]]}
