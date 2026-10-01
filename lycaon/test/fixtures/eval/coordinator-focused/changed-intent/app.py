import json
import sys
from report import summarize
print(json.dumps(summarize(json.load(sys.stdin))))
