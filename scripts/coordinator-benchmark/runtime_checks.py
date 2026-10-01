"""Check the application's declared scanner requirements before candidate entry."""


def requested_full_pass(response, project_id):
    overview = response['overview']
    if overview['project_id'] != project_id or overview.get('enabled') is not True:
        raise ValueError('full scan returned a different or disabled project')
    scanners = [scanner['id'] for scanner in overview['scanners']]
    if not scanners or len(set(scanners)) != len(scanners):
        raise ValueError('full scan has no unique scanner selection')
    if any(scanner.get('available') is not True for scanner in overview['scanners']):
        raise ValueError('full scan scanner is unavailable')
    passes = response['passes']
    if len(passes) != 1 or not passes[0].get('assessment_id'):
        raise ValueError('single-root fixture requires exactly one full pass')
    requested = {'project_id': project_id, 'assessment_id': passes[0]['assessment_id'],
                 'scanners': sorted(scanners)}
    pass_members(passes[0], requested)
    return requested


def pass_members(full_pass, requested):
    members = full_pass['members']
    names = [member['scanner_id'] for member in members]
    if sorted(names) != requested['scanners']:
        raise ValueError('full pass changed its requested scanner membership')
    return members


def project_scans(overview, requested):
    if overview['project_id'] != requested['project_id'] or overview.get('enabled') is not True:
        raise ValueError('scanner health belongs to a different or disabled project')
    matches = [overview[key] for key in ('running', 'last_full') if overview.get(key)
               and overview[key]['assessment_id'] == requested['assessment_id']]
    if len(matches) != 1:
        raise ValueError('requested full pass is missing or ambiguous')
    full_pass = matches[0]
    ready = True
    scan_ids = set()
    for member in pass_members(full_pass, requested):
        phase = member['phase']
        if phase in {'waiting_for_scanner', 'waiting_for_pass'}:
            ready = False
            continue
        if phase != 'started':
            raise ValueError('full pass scanner did not start: ' + member['scanner_id'])
        scan = member.get('scan') or {}
        if (not scan.get('id') or scan['id'] in scan_ids or scan.get('scanner_id') != member['scanner_id']
                or scan.get('assessment_id') != requested['assessment_id']):
            raise ValueError('full pass has an invalid scanner binding')
        scan_ids.add(scan['id'])
        if scan.get('status') not in {'pending', 'running', 'complete'}:
            raise ValueError('full pass scanner did not succeed: ' + member['scanner_id']
                             + ' (' + str(scan.get('status')) + ')')
        ready = ready and scan['status'] == 'complete'
    if not ready or not full_pass.get('completed_at'):
        return None
    return requested


def project_baseline(overview):
    if overview.get('enabled') is not True:
        raise ValueError('application scanners are disabled')
    scanners=overview.get('scanners',[])
    if not scanners or len({s['id'] for s in scanners})!=len(scanners):
        raise ValueError('application baseline has no unique scanner selection')
    if any(s.get('available') is not True for s in scanners):
        raise ValueError('application scanner is unavailable')
    baseline=overview.get('baseline')
    if baseline is None:
        return None
    if not baseline.get('snapshot_id') or baseline.get('unobserved_directories',0)!=0:
        raise ValueError('fixture baseline is incomplete')
    if overview.get('last_full') or overview.get('running'):
        raise ValueError('fixture unexpectedly starts with a full scan')
    return {'project_id':overview['project_id'],'snapshot_id':baseline['snapshot_id'],
            'scanners':sorted(s['id'] for s in scanners)}
