#!/usr/bin/env python3
"""Prepare private RN01 configuration from an explicitly exported Mihomo profile."""
import argparse
import importlib.util
import copy
import json
import os
import secrets
import subprocess
import tarfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / "work"
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--config', required=True, type=Path, help='Your exported full Clash/Mihomo JSON or YAML profile')
parser.add_argument('--rule-cache', type=Path, help='Directory containing the exported rule-provider cache files')
parser.add_argument('--selection-socket', help='Optional local Mihomo Unix controller socket to retain manual selections')
parser.add_argument('--output-dir', type=Path, default=ROOT/'private')
args = parser.parse_args()
PRIVATE = args.output_dir.resolve()
PRIVATE.mkdir(mode=0o700, exist_ok=True)
PRIVATE.chmod(0o700)


def private_write(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w") as out:
        out.write(data)


try:
    raw=args.config.read_text()
    if args.config.suffix.lower()=='.json': source=json.loads(raw)
    else:
        import yaml
        source=yaml.safe_load(raw)
except Exception:
    raise SystemExit('Cannot read profile; YAML input requires PyYAML. No profile content was logged.') from None
if not isinstance(source,dict) or not isinstance(source.get('proxies'),list) or not 1<=len(source['proxies'])<=2000:
    raise SystemExit('Export a full profile with 1..2000 inline proxy nodes; provider-only profiles need conversion first.')
if source.get('rule-providers') and args.rule_cache is None:
    raise SystemExit('--rule-cache is required when rule providers are present')
if any(str(g.get('name','')).startswith('__cdx_') for g in source.get('proxy-groups',[]) if isinstance(g,dict)):
    raise SystemExit('The __cdx_ namespace is reserved; use an original exported profile')
config = copy.deepcopy(source)
secret_path = PRIVATE / "secrets.json"
if secret_path.exists():
    credentials = json.loads(secret_path.read_text())
else:
    credentials = {key: secrets.token_urlsafe(32) for key in ("controller", "proxy", "agent")}
    private_write(secret_path, json.dumps(credentials))

selections = {}
try:
    if not args.selection_socket: raise ValueError('No optional selection socket requested')
    result = subprocess.run([
        "curl", "--unix-socket", args.selection_socket,
        "--noproxy", "*", "--max-time", "4", "-fsS", "http://localhost/proxies",
    ], capture_output=True, text=True, check=True)
    proxies = json.loads(result.stdout)["proxies"]
    selections = {name: info["now"] for name, info in proxies.items()
                  if info.get("type") == "Selector" and info.get("now")}
except (subprocess.SubprocessError, KeyError, ValueError):
    proxies = {}

real_names = [proxy["name"] for proxy in config.get("proxies", [])]
preferred = None
for selected in selections.values():
    seen = set()
    while selected in proxies and selected not in seen and proxies[selected].get("now"):
        seen.add(selected)
        selected = proxies[selected]["now"]
    if selected in real_names:
        preferred = selected
        break
ordered = ([preferred] if preferred else []) + [name for name in real_names if name != preferred]
config.setdefault("proxy-groups", []).append({
    "name": "__cdx_bootstrap", "type": "fallback", "proxies": ordered,
    "url": "https://www.gstatic.com/generate_204", "interval": 300, "lazy": True,
})

cache_root = (args.rule_cache or args.config.parent).resolve()
manifest = []
readiness={"schema":1,"providers":{}}
with tarfile.open(PRIVATE / "initial-rules.tgz", "w:gz", compresslevel=9) as archive:
    for index, (name, provider) in enumerate(config.get("rule-providers", {}).items()):
        original = (cache_root / provider["path"]).resolve()
        if not original.is_relative_to(cache_root) or not original.is_file():
            raise SystemExit("Rule cache is missing or outside the configured directory")
        try:
            import yaml
            fmt=provider.get("format") or "yaml"
            if fmt=="yaml":
                value=yaml.safe_load(original.read_text())
                if not isinstance(value,dict) or not any(k in value for k in ("payload","rules")):raise ValueError('Missing payload')
                lists=[value[k] for k in ("payload","rules") if k in value]
                if any(not isinstance(items,list) or any(not isinstance(item,str) for item in items) for items in lists):raise ValueError('Invalid payload')
                readiness["providers"][name]=int(any(bool(items) for items in lists))
            elif fmt=="text":
                readiness["providers"][name]=int(any(line.strip() and not line.lstrip().startswith(("#","//")) for line in original.read_text().splitlines()))
            elif fmt=="mrs":readiness["providers"][name]=1
            else:raise ValueError('Unsupported format')
        except Exception:
            raise SystemExit("Cannot inspect rule cache; install requirements.txt and supply an explicit valid payload. No cache content was logged.") from None
        relative = "rule-cache/r%03d.yaml" % index
        archive.add(original, arcname=relative, recursive=False)
        provider["path"] = "/tmp/codex-mihomo/" + relative
        provider["proxy"] = "__cdx_bootstrap"
        manifest.append({"name": name, "path": relative, "bytes": original.stat().st_size})
(PRIVATE / "initial-rules.tgz").chmod(0o600)
config.update({
    "port": 0, "socks-port": 0, "mixed-port": 7890,
    "redir-port": 7892, "tproxy-port": 7893,
    "allow-lan": True, "bind-address": "*", "ipv6": True,
    "external-controller": "127.0.0.1:9090", "secret": credentials["controller"],
    "authentication": ["router:" + credentials["proxy"]],
    "log-level": "warning", "find-process-mode": "off",
    "geo-auto-update": False, "lgbm-auto-update": False,
    "profile": {"store-selected": True, "store-fake-ip": False},
    "tun": {"enable": False},
})
for name in ("external-controller-unix", "external-controller-pipe", "external-controller-tls", "external-ui"):
    config.pop(name, None)
config["dns"] = {
    "enable": True, "listen": "[::]:1053", "ipv6": True,
    "enhanced-mode": "redir-host", "use-hosts": True, "use-system-hosts": True,
    "default-nameserver": ["223.5.5.5", "119.29.29.29"],
    "nameserver": ["https://1.1.1.1/dns-query#__cdx_bootstrap", "https://8.8.8.8/dns-query#__cdx_bootstrap"],
    "proxy-server-nameserver": ["https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"],
    "direct-nameserver": ["https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"],
    "nameserver-policy": {"+.lan": ["127.0.0.1:53"], "+.local": ["127.0.0.1:53"]},
}
spec=importlib.util.spec_from_file_location('router_policy',ROOT/'scripts/router-policy.py')
router_policy=importlib.util.module_from_spec(spec);spec.loader.exec_module(router_policy)
config,_policy_report=router_policy.optimize(config,preferred)
private_write(PRIVATE / "config.json", json.dumps(config, ensure_ascii=False, indent=2) + "\n")
private_write(PRIVATE / "routing-readiness.json",json.dumps(readiness))
private_write(PRIVATE / "selections.json", json.dumps(selections, ensure_ascii=False))
private_write(PRIVATE / "rule-cache-manifest.json", json.dumps(manifest, ensure_ascii=False, indent=2))
private_write(PRIVATE / "core-curl.conf", 'silent\nshow-error\nmax-time = 5\nheader = "Authorization: Bearer ' + credentials["controller"] + '"\n')
private_write(PRIVATE / "proxy-curl.conf", 'silent\nshow-error\nmax-time = 15\nproxy = "http://192.168.31.1:7890"\nproxy-user = "router:' + credentials["proxy"] + '"\n')
print(json.dumps({"nodes": len(real_names), "strategies": len(config["proxy-groups"]),
                  "rule_sets": len(manifest), "compressed_rule_bytes": (PRIVATE / "initial-rules.tgz").stat().st_size,
                  "saved_manual_selections": len(selections)}))
