#!/usr/bin/env python3
"""Read-only, bounded SSH sampling. No speed test or router configuration writes."""
import argparse
import json
import os
import pathlib
import re
import subprocess
import time

COMMAND = ('cat /proc/stat /proc/meminfo /proc/net/dev /proc/net/softnet_stat /proc/net/snmp; '
           'for p in $(pidof mihomo); do awk \'$1=="VmRSS:" {print "MihomoRSSKiB: "$2}\' /proc/$p/status; done')


def parse_snapshot(text):
    result = {'cpu': {}, 'interfaces': {}, 'softnet': [], 'tcp': {}, 'mihomoRSSKiB': 0}
    tcp_header = None
    for line in text.splitlines():
        fields = line.split()
        if re.match(r'^cpu(?:\d+)? ', line):
            result['cpu'][fields[0]] = list(map(int, fields[1:9]))
        elif line.startswith('MemAvailable:'):
            result['memoryAvailableKiB'] = int(fields[1])
        elif line.startswith('MihomoRSSKiB:'):
            result['mihomoRSSKiB'] += int(fields[1])
        elif line.startswith('Tcp:'):
            if len(fields) > 1 and fields[1].isdigit():
                if tcp_header:
                    result['tcp'] = dict(zip(tcp_header, map(int, fields[1:])))
            else:
                tcp_header = fields[1:]
        elif ':' in line and len(line.split(':', 1)[1].split()) == 16:
            name, raw = line.split(':', 1)
            if not re.fullmatch(r'[A-Za-z0-9_.:-]+', name.strip()):
                continue
            v = list(map(int, raw.split()))
            result['interfaces'][name.strip()] = dict(zip(
                ('rxBytes', 'rxPackets', 'rxErrors', 'rxDropped',
                 'txBytes', 'txPackets', 'txErrors', 'txDropped'), v[:4]+v[8:12]))
        elif re.match(r'^[0-9a-f]{8} ', line):
            result['softnet'].append(dict(zip(('processed', 'dropped', 'timeSqueeze'),
                                             [int(v, 16) for v in fields[:3]])))
    return result


def delta(before, after):
    result = {'cpu': {}, 'interfaces': {}, 'softnet': [], 'tcp': {}, 'counterReset': False}
    for key in ('OutSegs', 'RetransSegs', 'InErrs', 'OutRsts'):
        if key in before['tcp'] and key in after['tcp']:
            change = after['tcp'][key]-before['tcp'][key]
            if change >= 0:
                result['tcp'][key] = change
            else:
                result['counterReset'] = True
    for name, values in after['cpu'].items():
        old = before['cpu'].get(name)
        if old is None:
            continue
        d = [v-o for v, o in zip(values, old)]
        total = sum(d)
        if any(v < 0 for v in d) or total <= 0:
            result['counterReset'] = True
            continue
        result['cpu'][name] = {'busyPercent': round(100*(total-d[3]-d[4])/total, 2),
                               'softirqPercent': round(100*d[6]/total, 2)}
    for name, values in after['interfaces'].items():
        if name in before['interfaces']:
            d = {k: v-before['interfaces'][name][k] for k, v in values.items()}
            if any(v < 0 for v in d.values()):
                result['counterReset'] = True
            else:
                result['interfaces'][name] = d
    for old, new in zip(before['softnet'], after['softnet']):
        d = {k: v-old[k] for k, v in new.items()}
        if any(v < 0 for v in d.values()):
            result['counterReset'] = True
        else:
            result['softnet'].append(d)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', default='be3600pro', help='SSH host/alias, never a shell command')
    parser.add_argument('--ssh-config', required=True)
    parser.add_argument('--seconds', type=int, default=60)
    parser.add_argument('--interval', type=int, default=3)
    parser.add_argument('--output', required=True, help='New private JSONL file on this computer')
    args = parser.parse_args()
    if not 2 <= args.seconds <= 600 or not 2 <= args.interval <= 30:
        parser.error('seconds must be 2..600, interval 2..30')
    if not re.fullmatch(r'[A-Za-z0-9_.:-]+', args.host) or args.host.startswith('-'):
        parser.error('invalid SSH host')
    target = pathlib.Path(args.output)
    target.parent.mkdir(parents=True, exist_ok=True)
    # Exclusive creation prevents destroying a previous diagnostic record.
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    previous = None
    start = time.monotonic()
    with os.fdopen(fd, 'w', encoding='utf-8') as out:
        while True:
            stamp = time.monotonic()
            raw = subprocess.run(['ssh', '-F', args.ssh_config, '-o', 'BatchMode=yes',
                                  '-o', 'ConnectTimeout=4', args.host, COMMAND],
                                 capture_output=True, text=True, timeout=8, check=True)
            sample = parse_snapshot(raw.stdout)
            row = {'unixTime': time.time(), 'elapsedSeconds': round(stamp-start, 3),
                   'memoryAvailableKiB': sample.get('memoryAvailableKiB'),
                   'mihomoRSSKiB': sample['mihomoRSSKiB']}
            if previous is not None:
                row.update(delta(previous, sample))
            out.write(json.dumps(row, ensure_ascii=False)+'\n')
            out.flush()
            previous = sample
            if stamp-start >= args.seconds:
                break
            time.sleep(min(args.interval, max(0, args.seconds-(time.monotonic()-start))))
    print('Read-only samples saved:', target)


if __name__ == '__main__':
    main()
