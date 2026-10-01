"""Select a declared measurement bank before preparation or spending."""


def selected_operations(manifest, mode='exploration', tier='all', cases=None):
    operations = manifest['operations']
    selected = [op['id'] for op in operations if
                (op['role'] == 'calibration' if mode == 'calibration' else
                 op['role'] == 'scored' and tier in {'all', op.get('tier', 'gate')})]
    if cases is not None:
        requested = set(cases.split(','))
        if not requested or '' in requested or not requested <= set(selected):
            raise ValueError('case selection must name operations in the selected mode and tier')
        selected = [name for name in selected if name in requested]
    if not selected:
        raise ValueError('benchmark selection has no operations')
    return selected
