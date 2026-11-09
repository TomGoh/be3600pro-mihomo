#!/usr/bin/env python3
"""Publish a local diagnostic lease atomically. Does not contact or modify a router."""
import argparse
from pathlib import Path

from diagnostics.lease import heartbeat


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run-live',action='store_true',required=True,help='Authorize updating the supplied local run directory')
    parser.add_argument('--run-dir',type=Path,required=True)
    parser.add_argument('--token',required=True)
    parser.add_argument('--uptime-file',type=Path,default=Path('/proc/uptime'))
    args=parser.parse_args()
    if (args.run_dir/'stability-token').read_text().strip()!=args.token:parser.error('run owner changed; heartbeat not published')
    # The timestamp must use the guard host's uptime, never a wall-clock time.
    seconds=int(args.uptime_file.read_text().split()[0].split('.')[0])
    heartbeat(args.run_dir/'stability-lease',seconds)


if __name__=='__main__':main()
