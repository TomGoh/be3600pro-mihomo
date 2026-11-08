import hashlib
import base64
import socket
import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class RouterAccessTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location('router_access', ROOT/'scripts/router-access.py')
        cls.m = importlib.util.module_from_spec(spec); spec.loader.exec_module(cls.m)

    def test_web_login_challenge_uses_server_supplied_salt_and_client_mac(self):
        html = "var newEncryptMode = parseInt('1'); var Encrypt = { key: '0123456789abcdef0123456789abcdef' }; var deviceId = '02:11:22:33:44:55';"
        salt, device, algorithm = self.m.login_fields(html)
        self.assertEqual(device, '02:11:22:33:44:55')
        self.assertEqual(algorithm, 'sha256')
        h = lambda s: hashlib.sha256(s.encode()).hexdigest()
        self.assertEqual(self.m.password_digest('fixture-password', salt, 'fixture-nonce', algorithm), h('fixture-nonce'+h('fixture-password'+salt)))

    def test_unsupported_model_or_firmware_is_rejected(self):
        self.m.require_supported({'hardware':'RN01','romversion':'1.0.74'})
        for info in ({'hardware':'RD03','romversion':'1.0.74'}, {'hardware':'RN01','romversion':'1.0.75'}, {}):
            with self.assertRaises(ValueError): self.m.require_supported(info)

    def test_unknown_challenge_format_fails_instead_of_guessing(self):
        with self.assertRaises(ValueError): self.m.login_fields('unrecognized login page')

    def test_authenticated_luci_path_keeps_slash_before_session(self):
        r=self.m.Router(); r.token='0'*32
        calls=[]
        r.read=lambda url,data=None,**kw: calls.append(url) or b'{"code":0}'
        r.api('xqnetwork/diag_get_paras')
        self.assertIn('/cgi-bin/luci/;stok=',calls[0])

    def test_key_injection_is_rejected_and_private_key_never_accepted(self):
        for key in ('ssh-rsa bad; touch /tmp/bad','-----BEGIN PRIVATE KEY-----','ssh-ed25519 YWJj test'):
            with self.assertRaises(ValueError): self.m.checked_public_key(key)

    def test_applied_post_timeout_is_followed_by_cleanup_request(self):
        class Firmware:
            requested=[]
            def api(self,path,data=None,**kw):
                if data is None:return {'code':0,'iperf_test_thr':'25'}
                self.requested.append(data['option'])
                raise socket.timeout('firmware omitted HTTP response')
        r=Firmware()
        with self.m.Maintenance(r) as c:
            c.run('true')
        self.assertEqual(r.requested,['0','1'])
        self.assertEqual(c.original,'25')
        self.assertEqual(c.unacknowledged,2)


if __name__=='__main__': unittest.main()
