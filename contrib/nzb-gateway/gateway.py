"""Shared, persistent NZB acquisition policy for trusted media applications."""
import email.utils
import gzip
import hashlib
import hmac
import io
import json
import math
import os
import re
import sqlite3
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class Denied(Exception):
    def __init__(self, reason, retry=0, status=429):
        self.reason, self.retry, self.status = reason, max(0, math.ceil(retry)), status
        super().__init__(reason)


def retry_seconds(value, now):
    value = (value or '').strip()
    if value.isdigit():
        return max(0, int(value))
    try:
        parsed = email.utils.parsedate_to_datetime(value)
        if parsed.tzinfo is None:
            return 0
        return max(0, math.ceil(parsed.timestamp()-now))
    except (ValueError, TypeError, OverflowError):
        return 0


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


class Redirects(urllib.request.HTTPRedirectHandler):
    def __init__(self, policy, indexer):
        self.policy, self.indexer = policy, indexer

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        self.policy.check_url(newurl, self.indexer)
        delay = retry_seconds(headers.get('Retry-After'), self.policy.now())
        if delay:
            delay = self.policy.pause(self.indexer, delay)
            raise Denied('indexer_cooldown', delay)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class Policy:
    def __init__(self, path, config, now=time.time):
        self.config, self.now = config, now
        self.lock, self.serial = threading.RLock(), threading.Lock()
        self.db = sqlite3.connect(path, check_same_thread=False, isolation_level=None)
        self.db.execute('PRAGMA journal_mode=WAL')
        self.db.execute('PRAGMA busy_timeout=5000')
        self.db.executescript('''
          CREATE TABLE IF NOT EXISTS attempts(content TEXT,indexer TEXT,at REAL);
          CREATE INDEX IF NOT EXISTS attempts_content ON attempts(content,at);
          CREATE TABLE IF NOT EXISTS pauses(indexer TEXT PRIMARY KEY,until REAL);
          CREATE TABLE IF NOT EXISTS failures(key TEXT PRIMARY KEY,until REAL);
          CREATE TABLE IF NOT EXISTS cache(key TEXT PRIMARY KEY,data BLOB,name TEXT,expires REAL,seen REAL);
        ''')

    def close(self):
        self.db.close()

    def check_url(self, url, indexer):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme not in ('http', 'https') or parsed.username or parsed.password:
            raise Denied('unsupported_indexer_address', status=400)
        origins = self.config.get('manager_origins', [])
        origin = f'{parsed.scheme}://{parsed.netloc}'
        if origin in origins:
            match = re.fullmatch(r'(?:/api/v1/indexer)?/(\d+)/download/?', parsed.path)
            if match and match[1] == indexer:
                return
            raise Denied('unconfigured_manager_route', status=400)
        allowed = self.config['indexers'][indexer].get('hosts', [])
        if parsed.scheme != 'https' or parsed.netloc.lower() not in allowed:
            raise Denied('unconfigured_indexer_address', status=400)

    def identify(self, url, label):
        parsed = urllib.parse.urlsplit(url)
        origin = f'{parsed.scheme}://{parsed.netloc}'
        key = ''
        if origin in self.config.get('manager_origins', []):
            match = re.fullmatch(r'(?:/api/v1/indexer)?/(\d+)/download/?', parsed.path)
            if match:
                key = match[1]
        if not key:
            for identifier, item in self.config['indexers'].items():
                if parsed.netloc.lower() in item.get('hosts', []):
                    key = identifier
                    break
        # A label is never allowed to turn an unrecognised manager URL into an
        # authorised request. It is diagnostic metadata from a trusted client.
        if not key or key not in self.config['indexers'] or not self.config['indexers'][key].get('enabled', False):
            raise Denied('indexer_disabled_or_unrecognised', status=403)
        self.check_url(url, key)
        return key

    def pause(self, indexer, seconds):
        with self.lock:
            self.db.execute('INSERT INTO pauses VALUES(?,?) ON CONFLICT(indexer) DO UPDATE SET until=MAX(until,excluded.until)',
                            (indexer, self.now()+seconds))
            until = self.db.execute('SELECT until FROM pauses WHERE indexer=?', (indexer,)).fetchone()[0]
            return max(0, math.ceil(until-self.now()))

    def cached(self, key):
        with self.lock:
            row = self.db.execute('SELECT data,name FROM cache WHERE key=? AND expires>?', (key, self.now())).fetchone()
            if row:
                self.db.execute('UPDATE cache SET seen=? WHERE key=?', (self.now(), key))
                return row

    def reserve(self, key, content, indexer):
        now = self.now()
        window = int(self.config.get('window_seconds', 600))
        interval = int(self.config.get('min_interval_seconds', 30))
        limit = int(self.config.get('max_per_title', 3))
        with self.lock:
            self.db.execute('BEGIN IMMEDIATE')
            try:
                self.db.execute('DELETE FROM attempts WHERE at<=?', (now-window,))
                self.db.execute('DELETE FROM failures WHERE until<=?', (now,))
                self.db.execute('DELETE FROM pauses WHERE until<=?', (now,))
                pause = self.db.execute('SELECT until FROM pauses WHERE indexer=?', (indexer,)).fetchone()
                if pause and pause[0] > now:
                    raise Denied('indexer_cooldown', pause[0]-now)
                failure = self.db.execute('SELECT until FROM failures WHERE key=?', (key,)).fetchone()
                if failure and failure[0] > now:
                    raise Denied('release_cooldown', failure[0]-now)
                attempts = self.db.execute('SELECT indexer,at FROM attempts WHERE content=? ORDER BY at', (content,)).fetchall()
                own = next((at for provider, at in attempts if provider == indexer), None)
                if own is not None:
                    raise Denied('indexer_title_budget', own+window-now)
                if len(attempts) >= limit:
                    raise Denied('title_budget', attempts[0][1]+window-now)
                if attempts and attempts[-1][1]+interval > now:
                    raise Denied('spacing', attempts[-1][1]+interval-now)
                self.db.execute('INSERT INTO attempts VALUES(?,?,?)', (content, indexer, now))
                self.db.execute('COMMIT')
            except BaseException:
                self.db.execute('ROLLBACK')
                raise

    def store(self, key, data, name):
        now = self.now()
        with self.lock:
            self.db.execute('DELETE FROM cache WHERE expires<=?', (now,))
            self.db.execute('INSERT OR REPLACE INTO cache VALUES(?,?,?,?,?)',
                            (key, data, name, now+int(self.config.get('cache_seconds', 604800)), now))
            maximum = int(self.config.get('cache_bytes', 1000000000))
            size = self.db.execute('SELECT COALESCE(SUM(length(data)),0) FROM cache').fetchone()[0]
            while size > maximum:
                oldest = self.db.execute('SELECT key,length(data) FROM cache ORDER BY seen LIMIT 1').fetchone()
                self.db.execute('DELETE FROM cache WHERE key=?', (oldest[0],))
                size -= oldest[1]

    def fetch(self, item):
        url, content = str(item.get('url', '')), str(item.get('content_key', '')).strip().casefold()
        if not content or len(content) > 1000 or len(url) > 16384:
            raise Denied('invalid_request', status=400)
        indexer = self.identify(url, item.get('indexer', ''))
        # Stable release identifiers preserve the cache across rotating signed
        # URLs. Credentials and raw URLs are never persisted or logged.
        release = str(item.get('release_key') or url)
        key = digest(indexer+'|'+release)
        hit = self.cached(key)
        if hit:
            return *hit, True
        if not self.serial.acquire(timeout=1):
            raise Denied('spacing', 2)
        try:
            hit = self.cached(key)
            if hit:
                return *hit, True
            self.reserve(key, digest(content), indexer)
            try:
                data = self.download(url, indexer)
            except Denied:
                with self.lock:
                    self.db.execute('INSERT OR REPLACE INTO failures VALUES(?,?)', (key, self.now()+1800))
                raise
            name = re.sub(r'[^\w .()-]', '_', str(item.get('name') or 'release'))[:160]
            if not name.lower().endswith('.nzb'):
                name += '.nzb'
            self.store(key, data, name)
            return data, name, False
        finally:
            self.serial.release()

    def download(self, url, indexer):
        agent = self.config['indexers'][indexer].get('user_agent') or self.config.get('user_agent', 'SABnzbd/5.1.3')
        if '\r' in agent or '\n' in agent:
            raise Denied('invalid_client_configuration', status=500)
        request = urllib.request.Request(url, headers={'User-Agent': agent, 'Accept-Encoding': 'gzip'})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), Redirects(self, indexer))
        maximum = int(self.config.get('max_nzb_bytes', 50*1024*1024))
        try:
            with opener.open(request, timeout=25) as response:
                delay = retry_seconds(response.headers.get('Retry-After'), self.now())
                if delay:
                    self.pause(indexer, delay)
                data = response.read(maximum+1)
                encoding = response.headers.get('Content-Encoding', '').lower()
            if len(data) > maximum:
                raise Denied('nzb_too_large', status=502)
            if encoding == 'gzip':
                with gzip.GzipFile(fileobj=io.BytesIO(data)) as compressed:
                    data = compressed.read(maximum+1)
            root = ET.fromstring(data)
            if root.tag.rsplit('}', 1)[-1] == 'error' and root.get('code') in ('100','101','102','103','500','501'):
                delay = self.pause(indexer, 1800)
                raise Denied('indexer_cooldown', delay)
            if len(data) > maximum or root.tag.rsplit('}', 1)[-1] != 'nzb':
                raise Denied('invalid_nzb', status=502)
            return data
        except urllib.error.HTTPError as error:
            delay = retry_seconds(error.headers.get('Retry-After'), self.now())
            status = error.code
            error.close()
            if status in (401, 403, 429) or delay:
                delay = max(delay, 1800 if status in (401, 403) else 3600 if status == 429 and not delay else 60)
                delay = self.pause(indexer, delay)
                raise Denied('indexer_cooldown', delay) from None
            self.pause(indexer, 60)
            raise Denied('indexer_request_failed', status=502) from None
        except (urllib.error.URLError, TimeoutError, OSError, ET.ParseError, ValueError):
            self.pause(indexer, 60)
            raise Denied('indexer_request_failed', status=502) from None


def handler(policy, token):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            if self.path != '/health':
                self.send_error(404)
                return
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'ok')

        def do_POST(self):
            if self.path != '/v1/fetch':
                self.send_error(404)
                return
            if not hmac.compare_digest(self.headers.get('Authorization', ''), 'Bearer '+token):
                self.send_error(401)
                return
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if not 0 < length <= 32768:
                    raise Denied('invalid_request', status=400)
                item = json.loads(self.rfile.read(length))
                if not isinstance(item, dict):
                    raise Denied('invalid_request', status=400)
                data, name, cached = policy.fetch(item)
                self.send_response(200)
                self.send_header('Content-Type', 'application/x-nzb')
                self.send_header('Content-Length', str(len(data)))
                self.send_header('X-NZB-Cache', 'hit' if cached else 'miss')
                self.send_header('X-NZB-Filename', urllib.parse.quote(name))
                self.end_headers()
                self.wfile.write(data)
            except (Denied, ValueError, TypeError) as error:
                error = error if isinstance(error, Denied) else Denied('invalid_request', status=400)
                body = json.dumps({'error': error.reason, 'retry_after': error.retry}).encode()
                self.send_response(error.status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                if error.retry:
                    self.send_header('Retry-After', str(error.retry))
                self.end_headers()
                self.wfile.write(body)
    return Handler


if __name__ == '__main__':
    config = json.loads(Path(os.environ.get('NZB_GATEWAY_CONFIG', '/data/config.json')).read_text())
    token = Path(os.environ.get('NZB_GATEWAY_TOKEN_FILE', '/data/token')).read_text().strip()
    if len(token) < 32:
        raise SystemExit('Configure a gateway token with at least 32 characters.')
    policy = Policy(os.environ.get('NZB_GATEWAY_DATABASE', '/data/policy.sqlite'), config)
    ThreadingHTTPServer(('0.0.0.0', 8799), handler(policy, token)).serve_forever()
