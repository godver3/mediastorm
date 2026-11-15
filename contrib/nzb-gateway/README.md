# Optional NZB download policy

Failed availability checks, track probes, and playback retries can fetch several
NZBs for the same movie or episode within seconds. The optional policy routes
those acquisitions through a shared service with persistent download budgets,
cooldowns, and an NZB cache. It is disabled unless configured server-wide.

The service is included in this repository under `contrib/nzb-gateway`. It uses
Python's standard library and SQLite, without an external package or image
registry dependency beyond the Python base image. It is a companion container,
not an additional public service; the Compose overlay publishes no gateway port.

## Behavior

The default policy permits one new NZB per indexer and content identity in a
ten-minute window, at most three across all indexers, with at least 30 seconds
between acquisitions for that content. A rejected attempt does not consume a
slot. An upstream request that fails does consume its reserved slot. Existing
candidate resolution can continue to a different indexer within these limits;
the policy does not change search ordering or guarantee another indexer is next.

Catalog-based playback uses the title ID, media type, and episode identity,
independent of the profile. Direct API callers without catalog context use a
normalized release title. Title aliases can therefore produce different budgets
on those direct routes. The policy is shared by playback, availability checks,
and track probes, including externally configured Usenet engines that receive
the acquired NZB bytes.

The cache keys include the indexer and release GUID, falling back to the URL when
the GUID is absent. Cache hits do not consume another budget slot. The defaults
retain NZBs for seven days with a 1 GB total limit and a 50 MB per-file limit.
The SQLite database contains NZB bytes and filenames, but stores no raw request
URLs, API keys, or unhashed content identities. Protect and back up its volume
accordingly. Expired pauses and attempt windows are cleaned up automatically.

Both numeric and HTTP-date `Retry-After` values are retained without a short
maximum. Pauses survive restarts and later responses cannot shorten an existing
pause. HTTP 429 without a delay pauses an indexer for an hour. Authentication
errors pause it for at least 30 minutes. Failed releases cannot immediately be
retried under a different profile. Cache hits remain available during a pause.

Gateway requests require a bearer token. Indexer hosts, manager origins, and
download routes must be explicitly allowed, including redirect targets. Unknown
or disabled indexers are rejected before an upstream request. The service does
not log request URLs or forward its gateway bearer token to an indexer.

## Client identification and limits

MediaStorm already uses a SABnzbd User-Agent for ordinary NZB downloads and
supports per-indexer User-Agent overrides. This optional policy adds persistent
acquisition controls and a Python urllib download profile, using `gzip` and
`Connection: close`, without browser Fetch headers. Its User-Agent is configurable
globally and per indexer; the initial profile is `SABnzbd/5.1.3`.

This is not an anonymity or account-safety guarantee. Indexers still see request
timing, the source IP, searches, and NZB acquisitions. A Prowlarr proxy may apply
its own outgoing headers when it fetches an NZB instead of redirecting the client.
Search traffic is unchanged. The gateway does not reproduce a TLS fingerprint
or override an indexer's rules about permitted clients and usage.

## Docker Compose setup

Use a MediaStorm image built from a revision containing the Go integration.
Older images ignore the new environment variables and must not be used with
this overlay as a way to enforce the policy.

Create a private configuration directory and token on the host:

```sh
mkdir -m 700 nzb-gateway
cp contrib/nzb-gateway/config.example.json nzb-gateway/config.json
openssl rand -hex 32 > nzb-gateway/token
chmod 600 nzb-gateway/config.json nzb-gateway/token
```

Set `PUID` and `PGID` in `.env` to the owner of these files, then add:

```dotenv
NZB_POLICY_CONFIG_FILE=./nzb-gateway/config.json
NZB_POLICY_TOKEN_FILE=./nzb-gateway/token
```

Do not commit the private configuration or token. On an existing installation,
keep the UID and GID consistent with the ownership of its cache directory.

Edit `config.json` to match the configured indexers. The shipped example allows
none until an entry is explicitly enabled. For Prowlarr, `manager_origins` must
match the exact origin in result download URLs, and the `indexers` keys must
match Prowlarr's numeric IDs. For direct Newznab results, configure the HTTPS
hosts used by each indexer and any permitted redirect target. Multiple names for
one provider should share one entry so they share a budget. Set `user_agent`
inside an indexer entry when a provider requires a different client header.

The gateway accepts only Prowlarr download routes such as `/2/download` and
`/api/v1/indexer/2/download`; it does not proxy arbitrary manager paths. Private
Prowlarr installations can use their Docker service hostname or
`host.docker.internal` for a host installation. The example origin alone does
not change URLs returned by Prowlarr.

After verifying the configuration, start the opt-in deployment:

```sh
docker compose -f docker-compose.yml -f docker-compose.nzb-policy.yml up -d --build
```

The overlay requires a healthy gateway and sets `NZB_GATEWAY_REQUIRED=1`.
Missing gateway configuration, a stopped gateway, policy denials, and invalid
credentials return an acquisition error; they never trigger a direct download
fallback. Spacing waits honor caller cancellation and have a bounded lifetime.

For non-Compose deployments, set these server environment variables and ensure
the token file is readable by MediaStorm:

| Variable | Meaning |
| --- | --- |
| `NZB_GATEWAY_URL` | Internal HTTP(S) origin of the policy service |
| `NZB_GATEWAY_TOKEN_FILE` | Path to the shared token, at least 32 characters |
| `NZB_GATEWAY_REQUIRED` | Set to `1` to reject acquisitions when no URL is configured |

Changing policy configuration requires restarting the gateway. Removing the
overlay and the three server variables restores the ordinary direct acquisition
path. Preserve `nzb_policy_data` if the cache and active pauses should survive a
later re-enable; changing or deleting that volume resets this state.

## Tests

All new tests use loopback HTTP fixtures and synthetic credentials:

```sh
cd contrib/nzb-gateway
python3 -B -m unittest -v
cd ../../backend
go test ./internal/nzbfetch ./services/playback ./services/usenet ./internal/providerbreaker
go test ./handlers -run 'TestNZB|TestUsenet|TestPrequeue'
```

The gateway tests cover persistent budgets and cache reuse, concurrent duplicate
requests, long numeric and date-based cooldowns, header behavior, gzip handling,
authentication, and rejection of disabled indexers and unapproved redirects.
