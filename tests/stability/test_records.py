import json
from pathlib import Path
import sys
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'scripts'))
from diagnostics.records import EvidenceError, read_jsonl, analyze_http, evaluate_run


class EvidenceTests(unittest.TestCase):
    def row(self,**kw):
        return dict(event='request',worker=0,status=200,bytes=100,seconds=.1,ok=True,reused=True,**kw)

    def test_no_records_is_inconclusive(self):
        self.assertEqual(analyze_http([])['verdict'],'inconclusive')

    def test_real_error_is_not_hidden_by_a_planned_stop_label(self):
        r=self.row();r.update(status=500,ok=False,stopReason='duration',error='context deadline exceeded')
        self.assertEqual(analyze_http([r])['verdict'],'fail')

    def test_planned_duration_requires_bytes_and_time_coverage(self):
        r=self.row();r.update(ok=False,stopReason='duration',seconds=60,error='context deadline exceeded')
        self.assertEqual(analyze_http([r],minimum_seconds=59)['verdict'],'pass')
        self.assertEqual(analyze_http([r],minimum_seconds=61)['verdict'],'inconclusive')
        r['bytes']=0
        self.assertEqual(analyze_http([r])['verdict'],'fail')

    def test_budget_stop_does_not_claim_duration_coverage(self):
        r=self.row();r.update(ok=False,stopReason='budget',seconds=.2)
        self.assertEqual(analyze_http([r],minimum_seconds=10)['verdict'],'inconclusive')

    def test_external_cancel_is_not_a_planned_duration(self):
        r=self.row();r.update(ok=False,stopReason='cancelled',error='context canceled')
        self.assertEqual(analyze_http([r])['verdict'],'fail')

    def test_latency_percentiles_and_rate_use_actual_elapsed_time(self):
        rs=[]
        for n in [.1,.2,.3,1]:
            r=self.row();r['seconds']=n;rs.append(r)
        d=analyze_http(rs)
        self.assertEqual(d['medianMs'],250)
        self.assertEqual(d['p95Ms'],1000)
        self.assertAlmostEqual(d['transferredMbps'],.002)

    def test_malformed_truncated_and_nonfinite_json_are_rejected(self):
        with tempfile.TemporaryDirectory() as td:
            path=Path(td)/'records.jsonl'
            for raw in ['{"event":','{"seconds":NaN}\n','[]\n']:
                path.write_text(raw)
                with self.subTest(raw=raw),self.assertRaises(EvidenceError):read_jsonl(path)

    def test_invalid_numeric_types_do_not_become_zero(self):
        for value in [-1,True,float('nan'),'12']:
            r=self.row();r['seconds']=value
            with self.subTest(value=value),self.assertRaises(EvidenceError):analyze_http([r])

    def test_missing_footer_and_nonzero_exit_do_not_pass(self):
        rows=[dict(event='run-start',schema=1),self.row()]
        self.assertEqual(evaluate_run(rows)['verdict'],'inconclusive')
        rows.append(dict(event='run-end',completed=False,exitCode=1))
        self.assertEqual(evaluate_run(rows)['verdict'],'fail')

    def test_unexpected_disabled_proxy_fails_even_if_core_is_healthy(self):
        rows=[dict(event='run-start',schema=1),self.row(),dict(event='run-end',completed=True,exitCode=0)]
        status=[dict(coreRunning=True,enabled=False)]
        self.assertEqual(evaluate_run(rows,expected_enabled=True,status_samples=status)['verdict'],'fail')
        self.assertEqual(evaluate_run(rows,expected_enabled=True,status_samples=[])['verdict'],'inconclusive')

    def test_minimum_samples_prevents_a_single_result_claiming_a_soak(self):
        self.assertEqual(analyze_http([self.row()],minimum_samples=10)['verdict'],'inconclusive')

    def test_idle_wall_time_does_not_claim_a_long_request(self):
        rows=[dict(event='run-start',schema=1),self.row(),dict(event='run-end',completed=True,exitCode=0,elapsedSeconds=60)]
        self.assertEqual(evaluate_run(rows,minimum_seconds=59)['verdict'],'inconclusive')

    def test_missing_worker_records_do_not_pass(self):
        rows=[dict(event='run-start',schema=1,workers=2),self.row(),dict(event='run-end',completed=True,exitCode=0)]
        self.assertEqual(evaluate_run(rows)['verdict'],'inconclusive')

    def test_corrupt_progress_record_is_not_silently_ignored(self):
        bad=self.row();bad.update(event='progress',seconds='broken')
        with self.assertRaises(EvidenceError):analyze_http([self.row(),bad])

    def test_one_byte_and_a_long_wait_is_insufficient_transfer_evidence(self):
        r=self.row();r.update(ok=False,stopReason='duration',seconds=60,error='context deadline exceeded',bytes=1)
        self.assertEqual(analyze_http([r],minimum_seconds=59,minimum_bytes=1000)['verdict'],'inconclusive')

    def test_partial_failed_transfer_time_is_included_in_rate_denominator(self):
        good=self.row();good['seconds']=1
        bad=self.row();bad.update(ok=False,error='read failed',seconds=10)
        result=analyze_http([good,bad])
        self.assertEqual(result['verdict'],'fail')
        self.assertAlmostEqual(result['transferredMbps'],1600/11/1e6)

if __name__=='__main__':unittest.main()
