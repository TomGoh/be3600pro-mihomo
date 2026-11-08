#!/usr/bin/env python3
"""Actual LAN HTTPS API checks; fixtures and credentials never enter evidence logs."""
import argparse
import ipaddress
import hashlib
import http.server
import json
import pathlib
import ssl
import threading
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--run-live',action='store_true',required=True)
parser.add_argument('--listen-ip',required=True)
parser.add_argument('--pairing',type=pathlib.Path,default=ROOT/'private/router-pairing.json')
parser.add_argument('--certificate',type=pathlib.Path,default=ROOT/'private/server.crt')
args=parser.parse_args()
if ipaddress.ip_address(args.listen_ip) not in ipaddress.ip_network('192.168.31.0/24') or args.listen_ip in ('192.168.31.0','192.168.31.1','192.168.31.255'):parser.error('Choose this computer LAN IP in 192.168.31.0/24')
pair = json.loads(args.pairing.read_text())
context = ssl.create_default_context(cafile=str(args.certificate))
# OpenSSL's partial-chain flag permits this explicitly trusted, non-CA leaf anchor.
context.verify_flags |= getattr(ssl, 'VERIFY_X509_PARTIAL_CHAIN', 0x80000)
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
records = []

def api(method, path, body=None, token=None):
    data = None if body is None else json.dumps(body).encode()
    headers = {'Authorization': 'Bearer ' + (pair['token'] if token is None else token), 'Content-Type': 'application/json'}
    req = urllib.request.Request(pair['routerUrl'] + path, data=data, method=method, headers=headers)
    try:
        with opener.open(req, timeout=140) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)

def check(name, condition):
    records.append({'test': name, 'ok': bool(condition)})
    if not condition:
        raise RuntimeError('Integration assertion failed: ' + name)

class Feed(http.server.BaseHTTPRequestHandler):
    payload = b''
    def do_GET(self):
        if self.path != '/fixture':
            self.send_error(404); return
        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(self.payload)
    def log_message(self, *args):
        pass

subscription = None
initial_mode = None
httpd = None
try:
    status, body = api('GET', '/api/status', token='invalid-fixture-token')
    check('unauthenticated API rejected', status == 401)
    status, body = api('GET', '/api/status')
    check('authenticated HTTPS and running core', status == 200 and body['coreRunning'])
    check('proxy off before live API mutations',not body['enabled'])
    initial_mode = body['mode']
    status, groups = api('GET', '/api/groups')
    check('strategy groups available', status == 200 and len(groups['groups']) > 0)
    for mode in ['global', 'direct', 'rule']:
        status, body = api('PUT','/api/mode',{'mode':mode})
        check('persist mode ' + mode, status == 200 and body.get('mode') == mode)
    from urllib.parse import quote
    group = next(g for g in groups['groups'] if g['selectable'] and g['current'])
    status, body = api('PUT','/api/groups/'+quote(group['name'],safe=''),{'name':group['current']})
    check('encoded selector persisted',status == 200)
    nodes=[{'name':'integration-fixture-'+str(i),'type':'ss','server':'127.0.0.1','port':9,'cipher':'aes-128-gcm','password':'fixture-not-a-real-node'} for i in range(2)]
    Feed.payload = json.dumps({'proxies':nodes}).encode()
    httpd=http.server.ThreadingHTTPServer((args.listen_ip,18765),Feed)
    threading.Thread(target=httpd.serve_forever,daemon=True).start()
    status, body=api('POST','/api/subscriptions',{'name':'集成测试临时源','url':'http://'+args.listen_ip+':18765/fixture'})
    if status != 201: records.append({'stage':'source-add','http':status,'error':body.get('error','unknown')})
    check('real source added and loaded',status == 201 and body.get('subscription',{}).get('nodeCount')==2)
    subscription=body['subscription']['id']
    check('source URL redacted',body['subscription']['url']=='http://'+args.listen_ip+':18765/…')
    Feed.payload=json.dumps({'proxies':nodes[:1]}).encode()
    status,body=api('POST','/api/subscriptions/'+subscription+'/update',{})
    check('real source updated',status==200 and body.get('subscription',{}).get('nodeCount')==1)
    Feed.payload=b'proxies: []'
    status,body=api('POST','/api/subscriptions/'+subscription+'/update',{})
    check('invalid source update rejected',status==400)
    status,body=api('GET','/api/subscriptions')
    check('failed update kept last nodes',any(v['id']==subscription and v['nodeCount']==1 for v in body['subscriptions']))
    status,body=api('DELETE','/api/subscriptions/'+subscription)
    check('source deleted transactionally',status==200 and body.get('deleted'))
    subscription=None
finally:
    if subscription: api('DELETE','/api/subscriptions/'+subscription)
    if initial_mode: api('PUT','/api/mode',{'mode':initial_mode})
    if httpd: httpd.shutdown()
    destination=ROOT/'logs/agent/router-integration.json'
    destination.parent.mkdir(parents=True,exist_ok=True)
    destination.write_text(json.dumps(records,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(records,ensure_ascii=False))
