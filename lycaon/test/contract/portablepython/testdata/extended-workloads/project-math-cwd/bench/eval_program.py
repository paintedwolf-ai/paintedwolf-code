import json
from pathlib import Path

from sampling import pass_k

ratios = {
    "1,5,2": str(pass_k(1, 5, 2)),
    "5,5,2": str(pass_k(5, 5, 2)),
    "3,5,2": str(pass_k(3, 5, 2)),
}
invalid_rejected = False
try:
    pass_k(6, 5, 2)
except ValueError:
    invalid_rejected = True

Path("eval_result.json").write_text(
    json.dumps({"ratios": ratios, "invalid_input_rejected": invalid_rejected}, indent=2) + "\n",
    encoding="utf-8",
)
