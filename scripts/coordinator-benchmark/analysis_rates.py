"""Explicit, sourced operator estimates, separate from the application cost ledger."""
from decimal import Decimal
import json
import math


FIELDS = ['input', 'output', 'cache_read', 'cache_write', 'cache_write_1h']


def load(path):
    document=json.loads(path.read_text())
    if set(document)!={'version','rates'} or document['version']!=1:
        raise ValueError('unsupported operator rate card')
    rates={}
    for entry in document['rates']:
        if set(entry)!={'provider','model','source','observed_at','basis','usd_per_million'}:
            raise ValueError('operator rates require exact identities and source provenance')
        if not all(isinstance(entry[k],str) and entry[k] for k in ['provider','model','source','observed_at','basis']):
            raise ValueError('operator rate provenance is empty')
        values=entry['usd_per_million']
        if not isinstance(values,dict) or set(values)-set(FIELDS):
            raise ValueError('unknown pricing bucket')
        if any(type(v) not in [int,float] or not math.isfinite(v) or v<0 for v in values.values()):
            raise ValueError('rates must be finite, nonnegative USD amounts')
        key=(entry['provider'],entry['model'])
        if key in rates: raise ValueError('duplicate operator rate identity')
        rates[key]=entry
    return rates


def estimate(row, rate):
    prompt=row['prompt_tokens']; read=row['cache_read_tokens']; write=row['cache_write_tokens']; hour=row['cache_write_1h_tokens']
    buckets={'input':prompt-read-write,'output':row['completion_tokens'],'cache_read':read,
             'cache_write':write-hour,'cache_write_1h':hour}
    if any(type(v) is not int or v<0 for v in buckets.values()):
        raise ValueError('invalid normalized token accounting')
    prices=rate['usd_per_million']
    if any(count and key not in prices for key,count in buckets.items()):
        return None
    return float(sum(Decimal(count)*Decimal(str(prices.get(key,0))) for key,count in buckets.items())/Decimal(1000000))
