import copy,importlib.util,json,os,pathlib,subprocess,sys,tempfile,unittest
ROOT=pathlib.Path(__file__).resolve().parents[1]
class RouterPolicyTests(unittest.TestCase):
 def module(self):
  spec=importlib.util.spec_from_file_location('policy',ROOT/'scripts/router-policy.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
 def fixture(self):
  return {'secret':'canary-private-controller','proxies':[{'name':'n'+str(i)} for i in range(86)],'proxy-groups':[{'name':'User','type':'select','proxies':['n85']},{'name':'__cdx_bootstrap','type':'fallback','proxies':['n'+str(i) for i in range(86)],'interval':300}], 'dns':{'nameserver':['https://1.1.1.1/dns-query#__cdx_bootstrap'],'nameserver-policy':{'keep.invalid':['192.0.2.53']}},'rule-providers':{'domestic':{'behavior':'classical'},'ips':{'behavior':'ipcidr'},'foreign':{'behavior':'domain'}},'rules':['DOMAIN-SUFFIX,cn,DIRECT','DOMAIN,exact.invalid,DIRECT','RULE-SET,domestic,DIRECT','RULE-SET,ips,DIRECT','RULE-SET,foreign,User','AND,((NETWORK,TCP),(DOMAIN,conditional.invalid)),DIRECT','MATCH,User']}
 def test_bounded_bootstrap_and_direct_dns_preserve_routing_and_selected_node(self):
  old=self.fixture();before=copy.deepcopy(old);new,report=self.module().optimize(old,'n85')
  self.assertEqual(old,before);self.assertEqual(new['rules'],old['rules']);self.assertEqual(new['proxies'],old['proxies']);self.assertEqual(new['secret'],old['secret']);self.assertEqual(new['proxy-groups'][0],old['proxy-groups'][0])
  g=new['proxy-groups'][1];self.assertEqual(len(g['proxies']),4);self.assertEqual(g['proxies'][0],'n85');self.assertEqual(g['interval'],900);self.assertTrue(g['lazy']);self.assertLessEqual(g['timeout'],3000)
  dns=new['dns'];self.assertEqual(dns['direct-nameserver'],['127.0.0.1:53']);self.assertTrue(dns['direct-nameserver-follow-policy']);self.assertEqual(dns['nameserver-policy'],old['dns']['nameserver-policy']);self.assertEqual(dns['nameserver'],old['dns']['nameserver'])
  again,_=self.module().optimize(new,'n85');self.assertEqual(new,again)
 def test_invalid_preferred_node_cannot_mutate_input(self):
  old=self.fixture();before=copy.deepcopy(old)
  with self.assertRaises(ValueError):self.module().optimize(old,'not-a-node')
  self.assertEqual(old,before)
 def test_cli_is_dry_by_default_and_output_is_private_without_credential_logging(self):
  with tempfile.TemporaryDirectory() as d:
   p=pathlib.Path(d);source=p/'input.json';dest=p/'out.json';source.write_text(json.dumps(self.fixture()))
   cmd=[sys.executable,str(ROOT/'scripts/router-policy.py'),'--config',str(source),'--output',str(dest)]
   r=subprocess.run(cmd,capture_output=True,text=True);self.assertEqual(r.returncode,0,r.stderr);self.assertFalse(dest.exists());self.assertNotIn('canary-private',r.stdout+r.stderr)
   r=subprocess.run(cmd+['--write'],capture_output=True,text=True);self.assertEqual(r.returncode,0,r.stderr);self.assertEqual(dest.stat().st_mode&0o777,0o600);self.assertNotIn('canary-private',r.stdout+r.stderr)
   r=subprocess.run(cmd+['--write'],capture_output=True,text=True);self.assertNotEqual(r.returncode,0,'overwrote existing private file')
 def test_dns_map_is_identical_for_proxy_exceptions_and_both_rule_orders(self):
  for rules in [['DOMAIN,blocked.example.com,Proxy','DOMAIN-SUFFIX,example.com,DIRECT'],['DOMAIN-SUFFIX,example.com,DIRECT','DOMAIN,foo.example.com,Proxy'],['DOMAIN-SUFFIX,example.com,Proxy','DOMAIN,blocked.example.com,DIRECT'],['DOMAIN-KEYWORD,blocked,Proxy','DOMAIN-SUFFIX,example.com,DIRECT']]:
   for policy in [{},{'+.example.com':['192.0.2.53']},{'rule-set:foreign':['https://encrypted.invalid/dns-query']}]:
    with self.subTest(rules=rules,policy=policy):
     c=self.fixture();c['rules']=rules;c['dns']['nameserver-policy']=policy;n,_=self.module().optimize(c)
     self.assertEqual(n['dns']['nameserver-policy'],policy);self.assertEqual(n['rules'],rules)
