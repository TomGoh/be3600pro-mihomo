import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class RouterMetricsTest(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location('router_metrics', ROOT / 'scripts/router-metrics.py')
        self.m = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.m)

    def test_guest_time_is_not_counted_twice_and_each_core_is_visible(self):
        before = self.m.parse_snapshot('cpu 100 0 20 800 0 0 20 0 10 0\ncpu0 100 0 20 800 0 0 20 0 10 0\nMemAvailable: 1000 kB\n')
        after = self.m.parse_snapshot('cpu 120 0 30 860 0 0 30 0 20 0\ncpu0 120 0 30 860 0 0 30 0 20 0\nMemAvailable: 900 kB\n')
        r = self.m.delta(before, after)
        self.assertEqual(r['cpu']['cpu']['busyPercent'], 40.0)
        self.assertEqual(r['cpu']['cpu0']['softirqPercent'], 10.0)
        self.assertEqual(after['memoryAvailableKiB'], 900)

    def test_network_and_softnet_drops_are_deltas_not_lifetime_totals(self):
        a = self.m.parse_snapshot(' eth1: 100 2 3 4 0 0 0 0 200 5 6 7 0 0 0 0\n00000010 00000004 00000002 00000000\n')
        b = self.m.parse_snapshot(' eth1: 400 4 3 5 0 0 0 0 600 8 6 9 0 0 0 0\n00000020 00000005 00000002 00000000\n')
        r = self.m.delta(a, b)
        self.assertEqual(r['interfaces']['eth1']['rxBytes'], 300)
        self.assertEqual(r['interfaces']['eth1']['rxDropped'], 1)
        self.assertEqual(r['softnet'][0]['dropped'], 1)
        self.assertEqual(r['softnet'][0]['timeSqueeze'], 0)

    def test_counter_reset_is_reported_without_negative_measurements(self):
        a = self.m.parse_snapshot('cpu 100 0 0 900 0 0 0 0\n')
        b = self.m.parse_snapshot('cpu 1 0 0 9 0 0 0 0\n')
        self.assertTrue(self.m.delta(a, b)['counterReset'])
        self.assertNotIn('cpu', self.m.delta(a, b)['cpu'])

    def test_tcp_retransmits_and_actual_rss_are_reported(self):
        a = self.m.parse_snapshot('Tcp: OutSegs RetransSegs\nTcp: 100 4\nMihomoRSSKiB: 45000\n')
        b = self.m.parse_snapshot('Tcp: OutSegs RetransSegs\nTcp: 120 7\nMihomoRSSKiB: 47000\n')
        self.assertEqual(b['mihomoRSSKiB'], 47000)
        self.assertEqual(self.m.delta(a, b)['tcp']['RetransSegs'], 3)

    def test_udp_buffer_errors_are_deltas_not_lifetime_errors(self):
        a=self.m.parse_snapshot('Udp: InDatagrams RcvbufErrors SndbufErrors InCsumErrors\nUdp: 100 2103 0 0\nConntrack: 20\n')
        b=self.m.parse_snapshot('Udp: InDatagrams RcvbufErrors SndbufErrors InCsumErrors\nUdp: 130 2105 1 0\nConntrack: 24\n')
        self.assertEqual(b['conntrack'],24)
        self.assertEqual(self.m.delta(a,b)['udp']['RcvbufErrors'],2)
        self.assertEqual(self.m.delta(a,b)['udp']['SndbufErrors'],1)
        self.assertTrue(self.m.delta(b,a)['counterReset'])


if __name__ == '__main__':
    unittest.main()
