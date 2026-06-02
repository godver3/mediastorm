# Iroh Direct Spike

Minimal direct-only Iroh probe for the remote-access flow.

This intentionally disables relays and address lookup. The host prints an invite
containing its `EndpointAddr`; the client dials only the direct addresses in that
invite. That makes this a useful fit test for the "no relay" requirement.

## Commands

Terminal 1:

```sh
cd /Users/liamhughes/strmr/experiments/iroh-direct-spike
cargo run -- host --bind 0.0.0.0:0
```

Terminal 2:

```sh
cd /Users/liamhughes/strmr/experiments/iroh-direct-spike
cargo run -- client --invite '<INVITE_FROM_HOST>'
```

The client sends a tiny HTTP-like request over a bidirectional QUIC stream and
prints the response plus timing. If this fails off-LAN with relays disabled, that
is directly relevant to the product decision.
