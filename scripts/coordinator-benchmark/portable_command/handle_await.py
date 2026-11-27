"""Consumer handle await and chunked command_output drainer for running commands."""
import json


def parse_content_json(tool_result):
    body = (tool_result or {}).get('content') if (tool_result or {}).get('content') is not None else (tool_result or {}).get('result')
    if isinstance(body, dict):
        return body
    if isinstance(body, str):
        text = body.strip()
        while text.startswith('[') and '\n' in text:
            text = text.split('\n', 1)[1].strip()
        if text.startswith('{'):
            try:
                val = json.loads(text)
                return val if isinstance(val, dict) else {}
            except ValueError:
                return {}
    return {}


def _int_cursor(value):
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, str) and value.isdigit():
        return int(value)
    return None


def drain_command_output(application, session_id, handle, *, send_calls, start_cursor=0, max_pages=6):
    """Drain command_output pages using raw byte cursors, checking handle, monotonicity, and projection gaps."""
    cursor = start_cursor
    out_ids, stdout_parts, stderr_parts, gaps = [], [], [], []
    last_page = {}
    drained_all = False
    for _ in range(max_pages):
        o_cid, o_res = send_calls(application, session_id, [{'name': 'command_output', 'args': {'handle': handle, 'cursor': cursor}}])[0]
        out_ids.append(o_cid)
        if (o_res or {}).get('outcome') == 'rejected':
            gaps.append('command_output_rejected')
            break
        page = parse_content_json(o_res)
        if (not page or not isinstance(page.get('chunks'), list)
                or 'handle' not in page or 'cursor' not in page
                or 'next_cursor' not in page or not isinstance(page.get('running'), bool)):
            gaps.append('command_output_missing')
            break
        last_page = page
        if page.get('handle') != handle:
            gaps.append('command_output_cursor_gap')
            break
        if page.get('truncated') is True:
            gaps.append('command_output_truncated')
        p_cur = _int_cursor(page.get('cursor'))
        n_cur = _int_cursor(page.get('next_cursor'))
        if p_cur != cursor or n_cur is None or n_cur < cursor:
            gaps.append('command_output_cursor_gap')
            break
        prev_c = None
        bad_chunk = False
        for ch in page['chunks']:
            c_cur = _int_cursor(ch.get('cursor')) if isinstance(ch, dict) else None
            if c_cur is None or c_cur < cursor or c_cur >= n_cur or (prev_c is not None and c_cur <= prev_c):
                bad_chunk = True
                break
            prev_c = c_cur
            stream, text = ch.get('stream'), ch.get('text') or ''
            if stream == 'stdout':
                stdout_parts.append(text)
            elif stream == 'stderr':
                stderr_parts.append(text)
            else:
                gaps.append('command_output_unavailable')
        if bad_chunk:
            gaps.append('command_output_cursor_gap')
            break
        if n_cur == cursor:
            drained_all = True
            break
        cursor = n_cur
    if not drained_all and not gaps:
        gaps.append('command_output_page_limit')
    running = last_page.get('running')
    exit_code = last_page.get('exit_code')
    terminal = bool(running is False or isinstance(exit_code, int))
    return {
        'out_ids': out_ids, 'stdout': ''.join(stdout_parts), 'stderr': ''.join(stderr_parts),
        'next_cursor': cursor, 'running': running, 'exit_code': exit_code,
        'termination_reason': last_page.get('termination_reason') or '',
        'truncated': bool(last_page.get('truncated')), 'terminal': terminal,
        'gaps': list(dict.fromkeys(gaps)),
    }


def await_running_handle(application, session_id, cmd_result, *, send_calls, max_polls=2, max_pages=6, timeout_ms=120000):
    """Await a state=='running' command handle via wait + chunked command_output within model-turn budget."""
    payload = parse_content_json(cmd_result)
    if (payload.get('state') or (cmd_result or {}).get('outcome')) != 'running':
        return None
    handle = payload.get('handle') or payload.get('job_handle') or ((cmd_result or {}).get('data') or {}).get('handle') or (cmd_result or {}).get('handle') or ''
    if not handle:
        return {'awaited': False, 'handle': '', 'terminal': False, 'gap': 'running_command_without_handle', 'gaps': ['running_command_without_handle'], 'wait_call_ids': [], 'output_call_ids': [], 'final_output': {}}
    wait_ids, out_ids, stdout_all, stderr_all, gaps = [], [], [], [], []
    cursor, terminal, last_drained = 0, False, {}
    for _ in range(max_polls):
        w_cid, w_res = send_calls(application, session_id, [{
            'name': 'wait',
            'args': {'until_complete': True, 'timeout_ms': timeout_ms, 'conditions': [{'kind': 'process_done', 'handles': [handle]}], 'reason': 'Await running command handle completion'},
        }])[0]
        wait_ids.append(w_cid)
        w_pay = parse_content_json(w_res)
        if (w_res or {}).get('outcome') == 'rejected':
            gaps.append('command_wait_rejected')
            break
        timed_out = bool(w_pay.get('timed_out') is True or w_pay.get('outcome') in {'timeout', 'timed_out'})
        drained = drain_command_output(application, session_id, handle, send_calls=send_calls, start_cursor=cursor, max_pages=max_pages)
        out_ids.extend(drained['out_ids'])
        stdout_all.append(drained['stdout'])
        stderr_all.append(drained['stderr'])
        cursor = drained['next_cursor']
        gaps.extend(drained['gaps'])
        last_drained = drained
        if drained['terminal']:
            terminal = True
            break
        if timed_out:
            gaps.append('command_wait_timeout')
    if not terminal and not gaps:
        gaps.append('unresolved_command_handle')
    uniq_gaps = list(dict.fromkeys(gaps))
    final_output = {
        'stdout': ''.join(stdout_all), 'stderr': ''.join(stderr_all),
        'running': last_drained.get('running'), 'exit_code': last_drained.get('exit_code'),
        'termination_reason': last_drained.get('termination_reason') or '',
        'truncated': bool(last_drained.get('truncated')), 'next_cursor': cursor,
    }
    return {
        'awaited': True, 'handle': handle, 'terminal': terminal,
        'gap': uniq_gaps[0] if uniq_gaps else None, 'gaps': uniq_gaps,
        'wait_call_ids': wait_ids, 'output_call_ids': out_ids, 'final_output': final_output,
    }
