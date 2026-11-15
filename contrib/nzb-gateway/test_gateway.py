import concurrent.futures
import email.utils
import gzip
import json
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from gateway import Denied, Policy, handler, retry_seconds

NZB = b'<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>'


class GatewayTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.clock = [1800000000.0]
        self.requests = []
        owner = self
        class Recorder(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_GET(self):
                owner.requests.append({'path': self.path, 'headers': dict(self.headers)})
                if 'rate' in self.path:
                    self.send_response(429)
                    self.send_header('Retry-After', '7200' if 'date' not in self.path else email.utils.formatdate(owner.clock[0]+7200, usegmt=True))
                    self.end_headers();return
                if 'redirect' in self.path:
                    self.send_response(301);self.send_header('Location','http://127.0.0.1:1/private');self.end_headers();return
                self.send_response(200)
                if 'gzip' in self.path:self.send_header('Content-Encoding','gzip')
                self.end_headers()
                self.wfile.write(b'not an NZB' if 'invalid' in self.path else gzip.compress(NZB) if 'gzip' in self.path else NZB)
        self.server = ThreadingHTTPServer(('127.0.0.1',0),Recorder)
        self.thread = threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
        self.origin = f'http://127.0.0.1:{self.server.server_port}'
        self.config = {'manager_origins':[self.origin], 'indexers':{str(i):{'enabled':i!=5,'hosts':[]} for i in range(1,6)}}
        self.path = str(Path(self.temp.name)/'policy.sqlite')
        self.policy = Policy(self.path,self.config,lambda:self.clock[0])

    def tearDown(self):
        self.policy.close();self.server.shutdown();self.server.server_close();self.thread.join();self.temp.cleanup()

    def item(self,indexer=1,release='release',content='movie:1',suffix=''):
        return {'url':self.origin+f'/{indexer}/download?apikey=synthetic'+suffix,
                'content_key':content,'release_key':release,'name':'Fixture'}

    def denied(self,reason,item):
        with self.assertRaises(Denied) as raised:self.policy.fetch(item)
        self.assertEqual(raised.exception.reason,reason)
        return raised.exception

    def test_shared_persistent_title_and_indexer_budgets_with_cache(self):
        self.assertEqual(self.policy.fetch(self.item())[0],NZB)
        self.denied('indexer_title_budget',self.item(release='other'))
        self.assertEqual(self.denied('spacing',self.item(2)).retry,30)
        self.clock[0]+=30;self.policy.fetch(self.item(2))
        self.clock[0]+=30;self.policy.fetch(self.item(3))
        self.clock[0]+=30;self.denied('title_budget',self.item(4))
        self.policy.close();self.policy=Policy(self.path,self.config,lambda:self.clock[0])
        self.denied('title_budget',self.item(4))
        data,name,hit=self.policy.fetch(self.item(suffix='&rotated=1'))
        self.assertTrue(hit);self.assertEqual(data,NZB);self.assertEqual(len(self.requests),3)

    def test_long_retry_after_is_persisted_and_another_indexer_remains_available(self):
        self.assertEqual(self.denied('indexer_cooldown',self.item(suffix='&rate=1')).retry,7200)
        self.clock[0]+=61
        self.policy.close();self.policy=Policy(self.path,self.config,lambda:self.clock[0])
        self.assertEqual(self.denied('indexer_cooldown',self.item(release='new',content='movie:2')).retry,7139)
        self.policy.fetch(self.item(2,content='movie:2'))
        self.assertEqual(len(self.requests),2)

    def test_http_date_retry_after_is_not_capped(self):
        self.assertEqual(self.denied('indexer_cooldown',self.item(suffix='&rate=date')).retry,7200)
        self.assertEqual(retry_seconds('not a date',self.clock[0]),0)

    def test_sab_headers_gzip_and_concurrent_duplicate_coalescing(self):
        with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
            rows=list(pool.map(lambda _:self.policy.fetch(self.item(suffix='&gzip=1')),range(5)))
        self.assertTrue(all(row[0]==NZB for row in rows));self.assertEqual(len(self.requests),1)
        headers={k.lower():v for k,v in self.requests[0]['headers'].items()}
        self.assertEqual(headers['user-agent'],'SABnzbd/5.1.3')
        self.assertEqual(headers['accept-encoding'],'gzip');self.assertEqual(headers['connection'],'close')
        self.assertNotIn('sec-fetch-mode',headers)

    def test_disabled_indexer_unknown_origin_and_redirect_are_rejected_without_target_request(self):
        self.denied('indexer_disabled_or_unrecognised',self.item(5))
        self.denied('indexer_disabled_or_unrecognised',dict(self.item(),url='http://127.0.0.1:1/private'))
        self.assertEqual(len(self.requests),0)
        self.denied('unconfigured_indexer_address',self.item(suffix='&redirect=1'))
        self.assertEqual(len(self.requests),1)

    def test_failed_release_does_not_trigger_another_network_request(self):
        self.denied('indexer_request_failed',self.item(suffix='&invalid=1'))
        self.clock[0]+=601
        self.policy.close();self.policy=Policy(self.path,self.config,lambda:self.clock[0])
        self.assertEqual(self.denied('release_cooldown',self.item(suffix='&invalid=1')).retry,1199)
        self.assertEqual(len(self.requests),1)

    def test_http_authentication_and_policy_response_do_not_expose_upstream_access(self):
        token = 'synthetic-gateway-token-' * 3
        server = ThreadingHTTPServer(('127.0.0.1', 0), handler(self.policy, token))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        endpoint = f'http://127.0.0.1:{server.server_port}'
        try:
            with urllib.request.urlopen(endpoint + '/health') as response:
                self.assertEqual(response.status, 200)
            request = urllib.request.Request(endpoint + '/v1/fetch', data=json.dumps(self.item()).encode())
            with self.assertRaises(urllib.error.HTTPError) as raised:
                urllib.request.urlopen(request)
            self.assertEqual(raised.exception.code, 401)
            raised.exception.close()
            self.assertEqual(self.requests, [])

            request.add_header('Authorization', 'Bearer ' + token)
            with urllib.request.urlopen(request) as response:
                self.assertEqual(response.read(), NZB)
                self.assertEqual(response.headers['X-NZB-Cache'], 'miss')
            self.assertEqual(len(self.requests), 1)
            self.assertNotIn('Authorization', self.requests[0]['headers'])

            denied = urllib.request.Request(endpoint + '/v1/fetch', data=json.dumps(self.item(5)).encode(),
                                            headers={'Authorization': 'Bearer ' + token})
            with self.assertRaises(urllib.error.HTTPError) as raised:
                urllib.request.urlopen(denied)
            self.assertEqual(raised.exception.code, 403)
            self.assertEqual(json.load(raised.exception)['error'], 'indexer_disabled_or_unrecognised')
            raised.exception.close()
            self.assertEqual(len(self.requests), 1)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__=='__main__':unittest.main()
