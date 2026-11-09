#!/usr/bin/env python3
"""Prepare bounded RN01 DNS/bootstrap policy. Default: plan only, no router access."""
import argparse
import copy
import json
import os
from pathlib import Path

NATIVE_DNS=['127.0.0.1:53']

def optimize(config,preferred=None):
    if not isinstance(config,dict):raise ValueError("Configuration must be an object")
    result=copy.deepcopy(config)
    nodes={p.get('name') for p in result.get('proxies',[]) if isinstance(p,dict)}
    if preferred is not None and preferred not in nodes:raise ValueError('Preferred bootstrap node does not exist')
    before=after=0
    for group in result.get('proxy-groups',[]):
        if group.get('name')!='__cdx_bootstrap':continue
        if group.get('type')!='fallback':raise ValueError('Unexpected internal bootstrap group type')
        names=group.get('proxies',[])
        if not isinstance(names,list) or not names or any(not isinstance(n,str) or n not in nodes for n in names):raise ValueError('Invalid bootstrap candidate list')
        before=len(names)
        ordered=([preferred] if preferred else [])+[n for n in names if n!=preferred]
        group['proxies']=list(dict.fromkeys(ordered))[:4]
        group.update(interval=900,timeout=3000,lazy=True)
        after=len(group['proxies'])
    dns=result.setdefault('dns',{})
    if not isinstance(dns,dict):raise ValueError('DNS configuration must be an object')
    dns['direct-nameserver']=NATIVE_DNS[:]
    dns['direct-nameserver-follow-policy']=True
    policy=dns.setdefault('nameserver-policy',{})
    if not isinstance(policy,dict):raise ValueError('DNS policy must be an object')
    # DNS cannot evaluate arbitrary routing predicates or reproduce all precedence
    # relationships. Preserve this map exactly; choose stock DNS only after the
    # core actually selected DIRECT, following any existing explicit policy.
    return result,{'bootstrapCandidatesBefore':before,'bootstrapCandidatesAfter':after,'directDNS':'stock-loopback','dnsPolicyPreserved':True,'userRulesChanged':False,'userNodesChanged':False}

def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--config',required=True,type=Path);p.add_argument('--output',type=Path);p.add_argument('--preferred-node-file',type=Path);p.add_argument('--write',action='store_true');args=p.parse_args()
    try:
        config=json.loads(args.config.read_text());preferred=args.preferred_node_file.read_text().strip() if args.preferred_node_file else None
        output,report=optimize(config,preferred)
        if args.write:
            if not args.output:raise ValueError('--output is required with --write')
            fd=os.open(args.output,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
            with os.fdopen(fd,'w') as f:json.dump(output,f,ensure_ascii=False,separators=(',',':'));f.write('\n')
        report['action']='private file written' if args.write else 'plan only'
        print(json.dumps(report))
    except (ValueError,KeyError,TypeError,OSError):raise SystemExit('Policy preparation failed; no configuration or credential content was logged') from None
if __name__=='__main__':main()
