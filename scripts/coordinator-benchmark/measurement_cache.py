"""Cache an independent measurement only against its complete evidence identity."""
import json
from progress import save
from snapshot import file_hash, identity


def measure(capture, grading, inputs, evaluate):
    evidence = {str(path.relative_to(capture)): file_hash(path) for path in sorted(inputs)}
    key = identity({'grading': grading, 'evidence': evidence})
    path = capture / ('measurement-' + key + '.json')
    if path.exists():
        receipt = json.loads(path.read_text())
        if receipt['identity'] != key or receipt['result_sha256'] != identity(receipt['result']):
            raise ValueError('measurement receipt is corrupt')
        return receipt['result']
    result = evaluate()
    if result['outcome'] in {'passed', 'failed'}:
        save(path, {'identity': key, 'result': result, 'result_sha256': identity(result)})
    return result
