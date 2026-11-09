#!/usr/bin/env python3
"""Offline diagnostic report. Exit 0=pass, 1=confirmed failure, 2=inconclusive/invalid evidence."""
import argparse
import json
from pathlib import Path

from diagnostics.records import EvidenceError,evaluate_run,read_jsonl
from diagnostics.pcap import compare_udp,parse_pcap


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    modes=parser.add_subparsers(dest='mode',required=True)
    http=modes.add_parser('http',help='Analyze one netprobe HTTP JSONL run')
    http.add_argument('log',type=Path)
    http.add_argument('--minimum-samples',type=int,default=1)
    http.add_argument('--minimum-bytes',type=int,default=0)
    http.add_argument('--minimum-seconds',type=float,default=0,help='Minimum measured duration of a single request, for streaming tests')
    http.add_argument('--expected-enabled',choices=['true','false'])
    http.add_argument('--status-log',type=Path,help='JSONL API status snapshots; no credentials')
    pcap=modes.add_parser('pcap',help='Pair UDP probe identities from a classic PCAP')
    pcap.add_argument('--client-log',type=Path,required=True)
    pcap.add_argument('--capture',type=Path,required=True)
    pcap.add_argument('--peer-ip',required=True)
    pcap.add_argument('--peer-port',type=int,default=27051)
    pcap.add_argument('--capture-drops',type=int,help='Actual tcpdump kernel-drop counter; omit if unknown')
    args=parser.parse_args()
    try:
        if args.mode=='http':
            rows=read_jsonl(args.log)
            statuses=read_jsonl(args.status_log) if args.status_log else None
            expected=None if args.expected_enabled is None else args.expected_enabled=='true'
            result=evaluate_run(rows,expected_enabled=expected,status_samples=statuses,minimum_samples=args.minimum_samples,minimum_seconds=args.minimum_seconds,minimum_bytes=args.minimum_bytes)
        else:
            rows=read_jsonl(args.client_log)
            clients=[r for r in rows if r.get('event')=='udp']
            with args.capture.open('rb') as source:captured=parse_pcap(source,args.peer_ip,args.peer_port)
            result=compare_udp(clients,captured,args.capture_drops)
            start=[r for r in rows if r.get('event')=='run-start'];end=[r for r in rows if r.get('event')=='run-end']
            incomplete=len(start)!=1 or len(end)!=1
            if start:
                if start[0] is not rows[0] or start[0].get('mode')!='udp' or type(start[0].get('schema')) is not int or start[0]['schema']!=1:raise EvidenceError('invalid UDP run header')
                workers=start[0].get('workers')
                if isinstance(workers,bool) or not isinstance(workers,int) or not 1<=workers<=32:raise EvidenceError('invalid UDP worker count')
                incomplete|={r['worker'] for r in clients}!=set(range(workers))
            if end:
                footer=end[0]
                if footer is not rows[-1] or not isinstance(footer.get('completed'),bool) or type(footer.get('exitCode')) is not int:raise EvidenceError('invalid UDP run footer')
                if not footer['completed'] or footer['exitCode']!=0:result['verdict']='fail'
            if incomplete and result['verdict']!='fail':result['verdict']='inconclusive'
            result['runComplete']=not incomplete
        print(json.dumps(result,ensure_ascii=False,indent=2))
        return {'pass':0,'fail':1,'inconclusive':2}[result['verdict']]
    except (EvidenceError,OSError,UnicodeError,ValueError,KeyError) as error:
        print(json.dumps({'verdict':'inconclusive','error':str(error)},ensure_ascii=False))
        return 2


if __name__=='__main__':raise SystemExit(main())
