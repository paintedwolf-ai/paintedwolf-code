"""Record an OS write result from a controlled preflight command."""
import errno
import json
import shlex

PROGRAM = """import json, pathlib, sys
output, receipt, call = sys.argv[1:]
try:
    pathlib.Path(output).write_bytes(b'boundary probe')
except OSError as error:
    result = {'call': call, 'errno': error.errno}
else:
    result = {'call': call, 'errno': 0}
pathlib.Path(receipt).write_text(json.dumps(result))
"""


def command(target, receipt, call):
    return shlex.join(['python3', '-B', '-c', PROGRAM, str(target), str(receipt), call])


def require_denied(receipt, target, call):
    result = json.loads(receipt.read_text())
    if (result.get('call') != call or type(result.get('errno')) is not int
            or result['errno'] not in {errno.EACCES, errno.EPERM} or target.exists()):
        raise RuntimeError('ungranted write did not preserve the permission boundary: ' + json.dumps(result))
    return result
