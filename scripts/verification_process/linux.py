"""Linux verification process births, ownership and open-file identities."""
from pathlib import Path
import os
from . import ProcessObserver


class Observer(ProcessObserver):
    def native_birth(self, pid):
        root = Path('/proc') / str(pid)
        fields = (root / 'stat').read_text().rpartition(') ')[2].split()
        if len(fields) < 20:
            raise ValueError('verification process birth is unavailable')
        return {'pid': pid, 'parent_pid': int(fields[1]), 'start_time_ticks': int(fields[19]),
                'uid': root.stat().st_uid}

    def files(self, pid, limit):
        rows = []
        with os.scandir(Path('/proc') / str(pid) / 'fd') as descriptors:
            for index, descriptor in enumerate(descriptors):
                if index >= limit:
                    raise ValueError('scheduler process descriptor inventory exceeds bound')
                try:
                    info = descriptor.stat()
                except FileNotFoundError:
                    continue
                rows.append((info.st_dev, info.st_ino))
        return rows
