"""Observe a Python entry point's completion without interpreting its error prose."""
import json
import pathlib
import runpy
import sys


def execute(record, args):
    sys.argv = ['app.py', *args]
    sys.path.insert(0, '.')
    record.write_text(json.dumps({'uncaught_exception': False}))
    try:
        runpy.run_path('app.py', run_name='__main__')
    except SystemExit:
        raise
    except BaseException as exc:
        record.write_text(json.dumps({'uncaught_exception': True, 'exception': type(exc).__name__}))
        raise


if __name__ == '__main__':
    execute(pathlib.Path(sys.argv[1]), sys.argv[2:])
