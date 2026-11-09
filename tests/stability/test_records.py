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

    def stop(self,**updates):
        row=dict(event='worker-stop',worker=1,status=0,bytes=0,seconds=60,
                 ok=False,reused=True,ttfbMs=0,stopReason='duration')
        row.update(updates)
        return row

    def test_duration_worker_stop_is_not_a_response_sample_or_transfer(self):
        result=analyze_http([self.row(),self.stop()],minimum_samples=2)
        self.assertEqual(result['verdict'],'inconclusive')
        self.assertEqual(result['responseSamples'],1)
        self.assertEqual(result['completed'],1)
        self.assertEqual(result['plannedStops'],0)
        self.assertEqual(result['longestMeasuredRequestSeconds'],.1)
        self.assertEqual(result['transferredBytes'],100)
        self.assertAlmostEqual(result['transferredMbps'],.008)
        self.assertEqual(analyze_http([self.row(),self.stop()],minimum_seconds=59)['verdict'],'inconclusive')
        self.assertEqual(analyze_http([self.stop()])['verdict'],'inconclusive')

    def test_valid_worker_stops_complete_worker_coverage(self):
        for stop in [self.stop(),self.stop(status=200,stopReason='budget'),
                     self.stop(status=200,stopReason='budget',ttfbMs=1,sha256='e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')]:
            rows=[dict(event='run-start',schema=1,workers=2),self.row(),stop,
                  dict(event='run-end',completed=True,exitCode=0)]
            self.assertEqual(evaluate_run(rows)['verdict'],'pass')

    def test_worker_stop_cannot_hide_failure_or_response_evidence(self):
        for updates in [dict(status=500),dict(status=200),dict(bytes=1),dict(ok=True),
                        dict(error='context deadline exceeded'),dict(error='unexpected EOF'),
                        dict(stopReason='cancelled'),dict(stopReason='unknown'),dict(stopReason=''),
                        dict(ttfbMs=1),dict(sha256='abc'),dict(stopReason='budget',status=500),
                        dict(stopReason='budget',status=200,error='read failed'),
                        dict(stopReason='budget',status=200,sha256='abc')]:
            with self.subTest(updates=updates):
                self.assertEqual(analyze_http([self.row(),self.stop(**updates)])['verdict'],'fail')

    def test_malformed_worker_stop_fields_are_rejected(self):
        for updates in [dict(error=False),dict(stopReason=1),dict(ttfbMs='invalid'),dict(sha256=False)]:
            with self.subTest(updates=updates),self.assertRaises(EvidenceError):
                analyze_http([self.row(),self.stop(**updates)])

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
