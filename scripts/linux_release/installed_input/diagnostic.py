"""Drive a partial-package diagnostic with the same fixed VM input authority."""
import argparse
import json
from pathlib import Path
import secrets

from .controller import Bridge, adapter, run_managed
from .protocol import canonical


def main(arguments):
    from ..installed_campaign import command, absolute
    from ..installed_execution import read, current_tree
    from ..installed_evidence import source
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', type=Path, required=True)
    parser.add_argument('--candidate-inputs', required=True)
    parser.add_argument('--candidate-run-id', type=int, required=True)
    parser.add_argument('--candidate-artifact-id', type=int, required=True)
    parser.add_argument('--candidate-controller-commit', required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--source-tree', required=True)
    parser.add_argument('--driver-tree', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(arguments)
    target = read(args.target)
    command(target, ['true'])
    selected = adapter(target['input_adapter'])
    if selected is None or min(args.candidate_run_id, args.candidate_artifact_id) < 1:
        raise ValueError('physical input diagnostic requires an exact selected VM and producer IDs')
    if (target['environment_id'], target['variant_id']) != ('diagnostic-desktop', 'diagnostic-native-input'):
        raise ValueError('physical input diagnostic must remain separate from campaign variants')
    runtime, tree = source(args.source_commit), source(args.driver_tree)
    if current_tree(Path(__file__).resolve().parents[3]) != tree:
        raise ValueError('physical input diagnostic controller differs from the selected tree')
    request = {'format_version': 1, 'execution_nonce': secrets.token_hex(16), 'source_commit': runtime,
        'driver_tree': tree, 'artifact_id': target['artifact_id'], 'environment_id': target['environment_id'],
        'variant_id': target['variant_id']}
    arguments = ['./task', 'test:linux:installed', '--', '--diagnose-first-launch', '--continue-installed',
        '--candidate-inputs', absolute(args.candidate_inputs), '--artifact-id', target['artifact_id'],
        '--candidate-run-id', str(args.candidate_run_id), '--candidate-artifact-id', str(args.candidate_artifact_id),
        '--candidate-controller-commit', source(args.candidate_controller_commit), '--source-commit', runtime,
        '--source-tree', source(args.source_tree), '--driver-tree', tree,
        '--execution-nonce', request['execution_nonce'], '--output', absolute(target['output']),
        '--input-vm-id', selected['vm_id']]
    mailbox = command(target, ['python3', '-m', 'scripts.linux_release.installed_input.mailbox',
        '--binding', json.dumps(request, sort_keys=True, separators=(',', ':')), '--vm-id', selected['vm_id']])
    args.output.mkdir(parents=True, mode=0o700, exist_ok=False)
    (args.output / 'execution-request.json').write_bytes(canonical(request))
    with Bridge(selected, request, mailbox, args.output) as bridge:
        if bridge.actor.value['ssh'] != {key: target[key] for key in ('host', 'port', 'user')}:
            raise ValueError('physical input diagnostic lease differs from its selected guest endpoint')
        code = run_managed(bridge, command(target, arguments), args.output / 'managed-execution.log')
    (args.output / 'diagnostic-controller.json').write_bytes(canonical({
        'format_version': 1, 'execution': request, 'guest_managed_exit_code': code,
        'partial': True, 'qualification_authority': False, 'publication_authority': False}))
    return 0 if code == 0 else 2
