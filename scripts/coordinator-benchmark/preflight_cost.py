"""Include compatibility calls in private spending without counting them as trials."""
import json
import math
from analysis_rates import estimate


def spending(run, rates=None):
    total = {'priced_usd':0.0,'unpriced_calls':0,'calls':0,'prompt_tokens':0,'completion_tokens':0,'repriced_calls':0}
    for attempt in (run/'provider-preflight').glob('*/attempt-*'):
        path = attempt/'probe/result.json'
        if not path.exists():
            if (attempt/'launched.json').exists():
                total['unpriced_calls'] += 1
            continue
        receipt = json.loads(path.read_text())
        rate = (rates or {}).get((receipt['provider'],receipt['model']))
        for stage in receipt['stages']:
            usage = stage.get('usage',{})
            row = {'prompt_tokens':usage.get('prompt_tokens',0), 'completion_tokens':usage.get('completion_tokens',0),
                   'cache_read_tokens':usage.get('cache_read_input_tokens',0),
                   'cache_write_tokens':usage.get('cache_creation_input_tokens',0),
                   'cache_write_1h_tokens':usage.get('cache_creation_1h_input_tokens',0)}
            total['calls'] += 1
            total['prompt_tokens'] += row['prompt_tokens']
            total['completion_tokens'] += row['completion_tokens']
            quote = estimate(row,rate) if rate and usage.get('present') and not usage.get('incomplete') else None
            if quote is not None:
                total['priced_usd'] += quote
                total['repriced_calls'] += 1
                continue
            captured = receipt.get('costs', {}).get(stage.get('stage'), {})
            amount = captured.get('estimated_usd')
            if amount is not None:
                if type(amount) not in (int, float) or not math.isfinite(amount) or amount < 0:
                    raise ValueError('invalid application probe cost')
                total['priced_usd'] += amount
            if amount is None or captured.get('complete') is not True or not usage.get('present') or usage.get('incomplete'):
                total['unpriced_calls'] += 1
    return total
