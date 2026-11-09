"""Strict evidence parsing: absent or damaged evidence must never become a pass."""
import json
import math
from pathlib import Path
import statistics


class EvidenceError(ValueError):
    pass


def read_jsonl(path):
    rows=[]
    with Path(path).open(encoding='utf-8') as source:
        for line_number,line in enumerate(source,1):
            if not line.strip():continue
            if len(line)>1024*1024:raise EvidenceError(f'line {line_number}: record too large')
            try:
                row=json.loads(line,parse_constant=lambda _:(_ for _ in ()).throw(ValueError()))
            except (ValueError,UnicodeError) as e:
                raise EvidenceError(f'line {line_number}: invalid or truncated JSON') from e
            if not isinstance(row,dict):raise EvidenceError(f'line {line_number}: object required')
            rows.append(row)
    return rows


def number(value,name,integer=False):
    if isinstance(value,bool) or not isinstance(value,(int,float)) or not math.isfinite(value) or value<0 or integer and not isinstance(value,int):
        raise EvidenceError(f'{name}: nonnegative finite '+('integer' if integer else 'number')+' required')
    return value


def verdict(failed,incomplete):
    return 'fail' if failed else 'inconclusive' if incomplete else 'pass'


def analyze_http(rows,minimum_samples=1,minimum_seconds=0,minimum_bytes=0):
    number(minimum_samples,'minimum samples',True);number(minimum_seconds,'minimum seconds')
    number(minimum_bytes,'minimum bytes',True)
    if minimum_samples<1:raise EvidenceError('minimum samples must be positive')
    requests=[];failed=[];planned=[]
    for row in rows:
        if row.get('event') not in ('request','progress','worker-stop','run-start','run-end'):raise EvidenceError('unknown event type')
        if row['event'] in ('run-start','run-end'):continue
        for key in ('worker','status','bytes'):number(row.get(key),key,True)
        number(row.get('seconds'),'seconds')
        if not isinstance(row.get('ok'),bool) or not isinstance(row.get('reused'),bool):raise EvidenceError('boolean outcome and reuse evidence required')
        if 'unixTime' in row:number(row['unixTime'],'unixTime')
        if row['event']!='request':continue
        reason=row.get('stopReason','');error=row.get('error','')
        if not isinstance(error,str) or not isinstance(reason,str):raise EvidenceError('error/stopReason must be strings')
        allowed=row['status'] in (200,204,206)
        duration=reason=='duration' and error=='context deadline exceeded'
        budget=reason=='budget' and error==''
        if allowed and row['bytes']>0 and (duration or budget) and not row['ok']:
            planned.append(row)
        elif row['ok'] and allowed and not error and not reason:
            requests.append(row)
        else:failed.append(row)
    measured=requests+planned
    samples=[r['seconds']*1000 for r in requests]
    warm=[r['seconds']*1000 for r in requests if r['reused']]
    duration=max((r['seconds'] for r in measured),default=0)
    # Per-request elapsed time is not aggregate link throughput under concurrency.
    active=sum(r['seconds'] for r in measured+failed)
    total=sum(r['bytes'] for r in measured+failed)
    incomplete=len(measured)<minimum_samples or duration<minimum_seconds or sum(r['bytes'] for r in measured)<minimum_bytes
    return {'verdict':verdict(bool(failed),incomplete),'completed':len(requests),'plannedStops':len(planned),'failed':len(failed),
            'responseSamples':len(measured),'newConnections':sum(not r['reused'] for r in requests),
            'reusedRequests':sum(r['reused'] for r in requests),'transferredBytes':total,
            'medianMs':statistics.median(samples) if samples else None,
            'p95Ms':sorted(samples)[math.ceil(.95*len(samples))-1] if samples else None,
            'warmMedianMs':statistics.median(warm) if warm else None,
            'longestMeasuredRequestSeconds':duration,
            'transferredMbps':total*8/active/1e6 if active else None,
            'rateBasis':'sum of measured request durations; not aggregate wire throughput'}


def evaluate_run(rows,expected_enabled=None,status_samples=None,minimum_samples=1,minimum_seconds=0,minimum_bytes=0):
    result=analyze_http(rows,minimum_samples=minimum_samples,minimum_seconds=minimum_seconds,minimum_bytes=minimum_bytes)
    start=[r for r in rows if r.get('event')=='run-start'];end=[r for r in rows if r.get('event')=='run-end']
    incomplete=len(start)!=1 or len(end)!=1
    failed=result['verdict']=='fail'
    if start and (rows[0] is not start[0] or type(start[0].get('schema')) is not int or start[0]['schema']!=1):raise EvidenceError('invalid run header or schema')
    if start and 'workers' in start[0]:
        workers=number(start[0]['workers'],'workers',True)
        if not 1<=workers<=32:raise EvidenceError('invalid worker count')
        present={r['worker'] for r in rows if r.get('event') in ('request','worker-stop')}
        incomplete|=present!=set(range(workers))
    if end:
        footer=end[0]
        if rows[-1] is not footer:raise EvidenceError('records follow the run footer')
        if not isinstance(footer.get('completed'),bool):raise EvidenceError('completed must be boolean')
        number(footer.get('exitCode'),'exitCode',True)
        failed|=not footer['completed'] or footer['exitCode']!=0
        if minimum_seconds:
            elapsed=number(footer.get('elapsedSeconds'),'elapsedSeconds')
            incomplete|=elapsed<minimum_seconds
    if expected_enabled is not None:
        if not isinstance(expected_enabled,bool):raise EvidenceError('expected state must be boolean')
        if not status_samples:incomplete=True
        for sample in status_samples or []:
            if not isinstance(sample.get('enabled'),bool) or not isinstance(sample.get('coreRunning'),bool):raise EvidenceError('invalid router status sample')
            failed|=sample['enabled']!=expected_enabled or not sample['coreRunning']
    result['verdict']=verdict(failed,incomplete or result['verdict']=='inconclusive')
    result['runComplete']=not incomplete
    return result
