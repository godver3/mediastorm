use std::{
    collections::HashMap,
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant},
};

use anyhow::{Context, Result};
use base64::prelude::*;
use clap::{Parser, Subcommand};
use iroh::{
    endpoint::{presets, Builder, Connection, RecvStream, SendStream},
    protocol::{AcceptError, ProtocolHandler, Router},
    EndpointAddr, SecretKey,
};
use n0_error::StdResultExt;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
};

mod rendezvous;

const ALPN: &[u8] = b"strmr-remote-spike/iroh-direct/0";
const INVITE_PREFIX: &str = "mshost-iroh-";
const LEGACY_INVITE_PREFIX: &str = "mshost-iroh-direct-";
// The whole proxied request (request line + headers + body) is buffered in memory
// before being forwarded upstream, so this cap bounds per-stream memory. 64 KiB was
// too small: app requests carrying a JSON body (list/settings sync, uploads) exceeded
// it and quinn's read_to_end rejected the stream with "stream too long". 4 MiB covers
// those payloads while keeping a single misbehaving stream bounded.
const MAX_REQUEST_BYTES: usize = 4 * 1024 * 1024;
const MAX_RESPONSE_BYTES: usize = 1024 * 1024;
const SPEED_CHUNK_BYTES: usize = 1024 * 1024;
const STREAM_CHUNK_BYTES: usize = 64 * 1024;
const MAX_LOG_HEADER_BYTES: usize = 128 * 1024;

static HTTP_REQUEST_ID: AtomicU64 = AtomicU64::new(1);

#[derive(Debug, Parser)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
    Host {
        #[arg(long, default_value = "0.0.0.0:0")]
        bind: String,
        #[arg(long)]
        origin: Option<String>,
        /// File listing active connection codes (one per line). The host publishes a
        /// rendezvous record for each so clients can resolve the invite over the DHT
        /// without a reachable backend. The file is re-read periodically for changes.
        #[arg(long)]
        rendezvous_file: Option<String>,
        /// File holding the host's persistent iroh secret key. When set, the key is
        /// loaded from here (created on first run) so the host keeps a stable node ID
        /// across restarts. A stable node ID lets already-paired clients reconnect with
        /// a cached invite via iroh discovery, without re-resolving over the DHT. When
        /// unset, a fresh ephemeral identity is generated each start (legacy behaviour).
        #[arg(long)]
        secret_file: Option<String>,
    },
    Client {
        #[arg(long)]
        invite: String,
    },
    Speed {
        #[arg(long)]
        invite: String,
        #[arg(long, default_value_t = 256 * 1024 * 1024)]
        bytes: u64,
    },
    HttpSpeedHost {
        #[arg(long, default_value = "0.0.0.0:19092")]
        bind: String,
    },
    /// Publish a rendezvous record mapping a connection code to an invite blob (test helper).
    RendezvousPublish {
        #[arg(long)]
        code: String,
        #[arg(long)]
        invite: String,
    },
    /// Resolve a connection code to its invite blob over the public DHT (test helper).
    RendezvousResolve {
        #[arg(long)]
        code: String,
    },
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter("info,iroh=info")
        .with_target(false)
        .init();

    match Cli::parse().command {
        Command::Host {
            bind,
            origin,
            rendezvous_file,
            secret_file,
        } => {
            run_host(
                &bind,
                origin.as_deref(),
                rendezvous_file.as_deref(),
                secret_file.as_deref(),
            )
            .await
        }
        Command::Client { invite } => run_client(&invite).await,
        Command::Speed { invite, bytes } => run_speed_client(&invite, bytes).await,
        Command::HttpSpeedHost { bind } => run_http_speed_host(&bind).await,
        Command::RendezvousPublish { code, invite } => run_rendezvous_publish(&code, &invite).await,
        Command::RendezvousResolve { code } => run_rendezvous_resolve(&code).await,
    }
}

/// How often to refresh each code's DHT record. Must stay well under
/// [`rendezvous::RENDEZVOUS_TTL_SECS`] so records never lapse while a code is still pending.
const RENDEZVOUS_REPUBLISH: Duration = Duration::from_secs(15 * 60);
/// How often to re-read the codes file to pick up newly created / claimed / revoked invites.
const RENDEZVOUS_POLL: Duration = Duration::from_secs(5);

/// Parse the rendezvous file: one connection code per line, blanks and `#` comments ignored.
async fn read_rendezvous_codes(path: &str) -> Vec<String> {
    match tokio::fs::read_to_string(path).await {
        Ok(contents) => contents
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty() && !line.starts_with('#'))
            .map(ToOwned::to_owned)
            .collect(),
        Err(err) => {
            // Missing file just means no active invites yet; only log unexpected errors.
            if err.kind() != std::io::ErrorKind::NotFound {
                eprintln!("rendezvous_file read error path={path} error={err}");
            }
            Vec::new()
        }
    }
}

/// Background task: keep a DHT rendezvous record live for every active connection code.
///
/// Re-reads `path` every [`RENDEZVOUS_POLL`], publishes new codes immediately, refreshes
/// existing ones every [`RENDEZVOUS_REPUBLISH`], and forgets codes that leave the file
/// (their DHT records lapse on their own once the TTL passes).
async fn run_rendezvous_publisher(path: String, invite: String) {
    let dht = match rendezvous_dht() {
        Ok(dht) => dht,
        Err(err) => {
            eprintln!("rendezvous publisher disabled: {err}");
            return;
        }
    };
    let mut last_published: HashMap<String, Instant> = HashMap::new();
    loop {
        let codes = read_rendezvous_codes(&path).await;
        last_published.retain(|code, _| codes.contains(code));
        for code in &codes {
            let due = last_published
                .get(code)
                .map(|at| at.elapsed() >= RENDEZVOUS_REPUBLISH)
                .unwrap_or(true);
            if !due {
                continue;
            }
            match rendezvous::publish(&dht, code, &invite).await {
                Ok(()) => {
                    last_published.insert(code.clone(), Instant::now());
                    println!(
                        "rendezvous_published code_key={}",
                        rendezvous::derived_public_z32(code)
                    );
                }
                Err(err) => eprintln!("rendezvous_publish_error error={err}"),
            }
        }
        tokio::time::sleep(RENDEZVOUS_POLL).await;
    }
}

/// Load the host's persistent iroh secret key from `path`, creating and saving a fresh one
/// if the file is absent. The key is stored base64url-encoded (32 raw bytes). A stable key
/// keeps the node ID — and therefore the published invite — constant across host restarts.
async fn load_or_create_secret_key(path: &str) -> Result<SecretKey> {
    match tokio::fs::read_to_string(path).await {
        Ok(contents) => {
            let decoded = BASE64_URL_SAFE_NO_PAD
                .decode(contents.trim())
                .context("decode secret key file")?;
            let bytes: [u8; 32] = decoded
                .as_slice()
                .try_into()
                .map_err(|_| anyhow::anyhow!("secret key file must contain 32 bytes"))?;
            Ok(SecretKey::from_bytes(&bytes))
        }
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            let key = SecretKey::generate();
            let encoded = BASE64_URL_SAFE_NO_PAD.encode(key.to_bytes());
            tokio::fs::write(path, encoded.as_bytes())
                .await
                .context("write secret key file")?;
            // The key is equivalent to a private identity; keep it owner-only.
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))
                    .await
                    .context("restrict secret key file permissions")?;
            }
            Ok(key)
        }
        Err(err) => Err(err).context("read secret key file"),
    }
}

fn rendezvous_dht() -> Result<mainline::Dht> {
    mainline::Dht::builder()
        .build()
        .map_err(|err| anyhow::anyhow!("build mainline dht: {err}"))
}

async fn run_rendezvous_publish(code: &str, invite: &str) -> Result<()> {
    let dht = rendezvous_dht()?;
    println!("publishing under {}", rendezvous::derived_public_z32(code));
    rendezvous::publish(&dht, code, invite).await?;
    println!("published; record refreshes are the host's job in production");
    Ok(())
}

async fn run_rendezvous_resolve(code: &str) -> Result<()> {
    let dht = rendezvous_dht()?;
    println!("resolving {}", rendezvous::derived_public_z32(code));
    match rendezvous::resolve(&dht, code).await? {
        Some(invite) => {
            println!("invite={invite}");
            Ok(())
        }
        None => Err(anyhow::anyhow!("no rendezvous record found for code")),
    }
}

async fn run_http_speed_host(bind: &str) -> Result<()> {
    let listener = TcpListener::bind(bind).await?;
    println!("http_speed_host=http://{bind}/speed");
    println!("waiting for HTTP speed requests");

    loop {
        let (mut stream, peer) = listener.accept().await?;
        tokio::spawn(async move {
            let mut request = vec![0u8; MAX_REQUEST_BYTES];
            let bytes_read = match stream.read(&mut request).await {
                Ok(bytes_read) => bytes_read,
                Err(err) => {
                    eprintln!("http_speed_read_error peer={peer} error={err}");
                    return;
                }
            };
            let request_text = String::from_utf8_lossy(&request[..bytes_read]);
            let first_line = request_text.lines().next().unwrap_or("<empty>");
            let bytes = parse_speed_request(first_line).unwrap_or(32 * 1024 * 1024);

            let header = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: {bytes}\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n",
            );
            if let Err(err) = stream.write_all(header.as_bytes()).await {
                eprintln!("http_speed_header_error peer={peer} error={err}");
                return;
            }

            let started = Instant::now();
            let chunk = vec![0xa5; SPEED_CHUNK_BYTES];
            let mut sent = 0u64;
            while sent < bytes {
                let remaining = (bytes - sent) as usize;
                let next = remaining.min(chunk.len());
                if let Err(err) = stream.write_all(&chunk[..next]).await {
                    eprintln!("http_speed_write_error peer={peer} error={err}");
                    return;
                }
                sent += next as u64;
            }
            let elapsed = started.elapsed();
            let seconds = elapsed.as_secs_f64().max(0.001);
            println!(
                "http_speed_sent peer={peer} bytes={sent} elapsed_ms={} speed_mbps={:.2}",
                elapsed.as_millis(),
                (sent as f64 * 8.0 / 1_000_000.0) / seconds
            );
        });
    }
}

async fn run_host(
    bind: &str,
    origin: Option<&str>,
    rendezvous_file: Option<&str>,
    secret_file: Option<&str>,
) -> Result<()> {
    let mut builder = relay_enabled_builder()
        .bind_addr(bind)?
        .alpns(vec![ALPN.to_vec()]);
    match secret_file {
        Some(path) => {
            let key = load_or_create_secret_key(path).await?;
            println!("host_secret_key=persisted path={path}");
            builder = builder.secret_key(key);
        }
        None => println!("host_secret_key=ephemeral"),
    }
    let endpoint = builder.bind().await?;

    let router = Router::builder(endpoint)
        .accept(
            ALPN,
            HttpProbe {
                origin: origin.map(ToOwned::to_owned),
            },
        )
        .spawn();

    tokio::time::timeout(Duration::from_secs(30), router.endpoint().online())
        .await
        .context("timed out waiting for relay registration")?;
    let addr = router.endpoint().addr();
    let invite = encode_invite(&addr)?;

    println!("host_id={}", router.endpoint().id());
    println!("host_addr={addr:?}");
    println!("invite={invite}");
    println!("relay_mode=default");
    println!("address_lookup=n0");
    println!("origin={}", origin.unwrap_or("(none)"));
    println!("waiting for iroh client requests");

    if let Some(path) = rendezvous_file {
        let path = path.to_string();
        let invite = invite.clone();
        tokio::spawn(async move { run_rendezvous_publisher(path, invite).await });
    }

    tokio::signal::ctrl_c().await?;
    router.shutdown().await?;
    Ok(())
}

async fn run_client(invite: &str) -> Result<()> {
    let addr = decode_invite(invite)?;
    let endpoint = relay_enabled_builder().bind().await?;

    println!("client_id={}", endpoint.id());
    println!("dial_addr={addr:?}");
    println!("relay_mode=default");
    println!("address_lookup=n0");

    let started = Instant::now();
    let conn = endpoint.connect(addr, ALPN).await?;
    let connected_in = started.elapsed();
    let (mut send, mut recv) = conn.open_bi().await?;

    send.write_all(b"GET /settings HTTP/1.1\r\nHost: strmr.local\r\n\r\n")
        .await?;
    send.finish()?;

    let response = recv.read_to_end(MAX_RESPONSE_BYTES).await?;
    let elapsed = started.elapsed();
    println!("connected_in_ms={}", connected_in.as_millis());
    println!("roundtrip_ms={}", elapsed.as_millis());
    println!("response_bytes={}", response.len());
    println!("{}", String::from_utf8_lossy(&response));

    conn.close(0u32.into(), b"done");
    endpoint.close().await;
    Ok(())
}

async fn run_speed_client(invite: &str, bytes: u64) -> Result<()> {
    let addr = decode_invite(invite)?;
    let endpoint = relay_enabled_builder().bind().await?;

    println!("client_id={}", endpoint.id());
    println!("dial_addr={addr:?}");
    println!("requested_bytes={bytes}");
    println!("relay_mode=default");
    println!("address_lookup=n0");

    let started = Instant::now();
    let conn = endpoint.connect(addr, ALPN).await?;
    let connected_in = started.elapsed();
    let (mut send, mut recv) = conn.open_bi().await?;

    let request = format!("GET /speed?bytes={bytes} HTTP/1.1\r\nHost: strmr.local\r\n\r\n");
    send.write_all(request.as_bytes()).await?;
    send.finish()?;

    let mut received = 0u64;
    while let Some(chunk) = recv.read_chunk(SPEED_CHUNK_BYTES).await? {
        received += chunk.bytes.len() as u64;
    }

    let elapsed = started.elapsed();
    let seconds = elapsed.as_secs_f64();
    let mib = received as f64 / 1024.0 / 1024.0;
    println!("connected_in_ms={}", connected_in.as_millis());
    println!("elapsed_ms={}", elapsed.as_millis());
    println!("received_bytes={received}");
    println!("speed_mib_s={:.2}", mib / seconds);
    println!(
        "speed_mbps={:.2}",
        (received as f64 * 8.0 / 1_000_000.0) / seconds
    );

    conn.close(0u32.into(), b"done");
    endpoint.close().await;
    Ok(())
}

fn encode_invite(addr: &EndpointAddr) -> Result<String> {
    let json = serde_json::to_vec(addr)?;
    Ok(format!(
        "{INVITE_PREFIX}{}",
        BASE64_URL_SAFE_NO_PAD.encode(json)
    ))
}

fn decode_invite(invite: &str) -> Result<EndpointAddr> {
    let encoded = invite
        .strip_prefix(LEGACY_INVITE_PREFIX)
        .or_else(|| invite.strip_prefix(INVITE_PREFIX))
        .context(format!("invite must start with {INVITE_PREFIX}"))?;
    let json = BASE64_URL_SAFE_NO_PAD.decode(encoded)?;
    Ok(serde_json::from_slice(&json)?)
}

fn relay_enabled_builder() -> Builder {
    Builder::new(presets::N0)
}

#[derive(Debug, Clone)]
struct HttpProbe {
    origin: Option<String>,
}

impl ProtocolHandler for HttpProbe {
    async fn accept(&self, connection: Connection) -> std::result::Result<(), AcceptError> {
        let remote = connection.remote_id();
        println!("accepted remote_id={remote}");

        loop {
            let (send, recv) = match connection.accept_bi().await {
                Ok(streams) => streams,
                Err(err) => {
                    println!("connection_stream_accept_closed remote_id={remote} error={err}");
                    break;
                }
            };
            let origin = self.origin.clone();
            tokio::spawn(async move {
                if let Err(err) = handle_iroh_http_stream(send, recv, origin, remote).await {
                    eprintln!("stream_error remote_id={remote} error={err}");
                }
            });
        }

        Ok(())
    }
}

async fn handle_iroh_http_stream(
    mut send: SendStream,
    mut recv: RecvStream,
    origin: Option<String>,
    remote: iroh::PublicKey,
) -> Result<()> {
    let request_id = HTTP_REQUEST_ID.fetch_add(1, Ordering::Relaxed);
    let started = Instant::now();
    let request = recv.read_to_end(MAX_REQUEST_BYTES).await.anyerr()?;
    let request_text = String::from_utf8_lossy(&request);
    let first_line = request_text.lines().next().unwrap_or("<empty>");
    let request_bytes = request.len();
    println!(
        "request id={request_id} remote_id={remote} line={first_line:?} bytes={request_bytes} content_length={} range={}",
        header_value(&request_text, "content-length").unwrap_or("-"),
        header_value(&request_text, "range").unwrap_or("-")
    );

    if let Some(bytes) = parse_speed_request(first_line) {
        let chunk = vec![0xa5; SPEED_CHUNK_BYTES];
        let mut sent = 0u64;
        while sent < bytes {
            let remaining = (bytes - sent) as usize;
            let next = remaining.min(chunk.len());
            send.write_all(&chunk[..next]).await.anyerr()?;
            sent += next as u64;
        }
        send.finish()?;
        let elapsed = started.elapsed();
        let seconds = elapsed.as_secs_f64().max(0.001);
        let mib = sent as f64 / 1024.0 / 1024.0;
        println!(
            "speed_sent id={request_id} bytes={sent} elapsed_ms={} speed_mib_s={:.2} speed_mbps={:.2}",
            elapsed.as_millis(),
            mib / seconds,
            (sent as f64 * 8.0 / 1_000_000.0) / seconds
        );
        return Ok(());
    }

    if let Some(origin) = &origin {
        if let Err(err) = proxy_raw_http_stream(request_id, origin, &request, &mut send).await {
            eprintln!(
                "proxy_error id={request_id} elapsed_ms={} error={err}",
                started.elapsed().as_millis()
            );
            let response = format!(
                "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                err.to_string().len(),
                err
            );
            send.write_all(response.as_bytes()).await.anyerr()?;
        }
        send.finish()?;
        return Ok(());
    }

    let body = br#"{"ok":true,"transport":"iroh-direct","probe":"settings"}"#;
    let headers = format!(
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    send.write_all(headers.as_bytes()).await.anyerr()?;
    send.write_all(body).await.anyerr()?;
    send.finish()?;
    Ok(())
}

async fn proxy_raw_http_stream(
    request_id: u64,
    origin: &str,
    request: &[u8],
    send: &mut SendStream,
) -> Result<()> {
    let started = Instant::now();
    let (host, port) = parse_http_origin(origin)?;
    println!("proxy_connect id={request_id} target={host}:{port}");
    let mut upstream = TcpStream::connect((host.as_str(), port)).await?;
    println!(
        "proxy_connected id={request_id} elapsed_ms={}",
        started.elapsed().as_millis()
    );
    let upstream_request = force_connection_close(request, &host, port);
    upstream.write_all(&upstream_request).await?;
    println!(
        "proxy_request_sent id={request_id} bytes={} elapsed_ms={}",
        upstream_request.len(),
        started.elapsed().as_millis()
    );

    let mut buf = vec![0u8; STREAM_CHUNK_BYTES];
    let mut total_bytes = 0usize;
    let mut body_bytes = 0usize;
    let mut header_buf = Vec::new();
    let mut header_logged = false;
    let mut first_body_logged = false;

    loop {
        let read = upstream.read(&mut buf).await?;
        if read == 0 {
            println!(
                "proxy_complete id={request_id} total_bytes={total_bytes} body_bytes={body_bytes} elapsed_ms={}",
                started.elapsed().as_millis()
            );
            return Ok(());
        }
        total_bytes += read;
        if !header_logged {
            let remaining = MAX_LOG_HEADER_BYTES.saturating_sub(header_buf.len());
            if remaining > 0 {
                header_buf.extend_from_slice(&buf[..read.min(remaining)]);
            }
            if let Some(header_end) = find_header_end(&header_buf) {
                let header_text = String::from_utf8_lossy(&header_buf[..header_end]);
                let status = header_text.lines().next().unwrap_or("<empty>");
                println!(
                    "proxy_response id={request_id} status={status:?} content_length={} content_range={} content_type={} elapsed_ms={}",
                    header_value(&header_text, "content-length").unwrap_or("-"),
                    header_value(&header_text, "content-range").unwrap_or("-"),
                    header_value(&header_text, "content-type").unwrap_or("-"),
                    started.elapsed().as_millis()
                );
                body_bytes += header_buf.len().saturating_sub(header_end);
                header_logged = true;
            } else if header_buf.len() >= MAX_LOG_HEADER_BYTES {
                println!(
                    "proxy_response_header_too_large id={request_id} buffered={} elapsed_ms={}",
                    header_buf.len(),
                    started.elapsed().as_millis()
                );
                header_logged = true;
            }
        } else {
            body_bytes += read;
        }
        if header_logged && !first_body_logged && body_bytes > 0 {
            println!(
                "proxy_first_body id={request_id} body_bytes={body_bytes} elapsed_ms={}",
                started.elapsed().as_millis()
            );
            first_body_logged = true;
        }
        send.write_all(&buf[..read]).await.anyerr()?;
    }
}

fn force_connection_close(request: &[u8], host: &str, port: u16) -> Vec<u8> {
    let Some(header_end) = find_header_end(request) else {
        return request.to_vec();
    };

    let headers = String::from_utf8_lossy(&request[..header_end]);
    let body = &request[header_end..];
    let mut lines = headers.split("\r\n");
    let Some(request_line) = lines.next() else {
        return request.to_vec();
    };

    let mut rewritten = String::with_capacity(headers.len() + 32);
    rewritten.push_str(request_line);
    rewritten.push_str("\r\n");

    let mut saw_host = false;
    for line in lines {
        if line.is_empty() {
            continue;
        }
        let Some((name, _value)) = line.split_once(':') else {
            rewritten.push_str(line);
            rewritten.push_str("\r\n");
            continue;
        };
        if name.eq_ignore_ascii_case("connection") || name.eq_ignore_ascii_case("proxy-connection")
        {
            continue;
        }
        if name.eq_ignore_ascii_case("host") {
            saw_host = true;
            rewritten.push_str("Host: ");
            rewritten.push_str(host);
            if port != 80 {
                rewritten.push(':');
                rewritten.push_str(&port.to_string());
            }
            rewritten.push_str("\r\n");
            continue;
        }
        rewritten.push_str(line);
        rewritten.push_str("\r\n");
    }

    if !saw_host {
        rewritten.push_str("Host: ");
        rewritten.push_str(host);
        if port != 80 {
            rewritten.push(':');
            rewritten.push_str(&port.to_string());
        }
        rewritten.push_str("\r\n");
    }
    rewritten.push_str("Connection: close\r\n\r\n");

    let mut out = rewritten.into_bytes();
    out.extend_from_slice(body);
    out
}

fn find_header_end(buf: &[u8]) -> Option<usize> {
    buf.windows(4)
        .position(|window| window == b"\r\n\r\n")
        .map(|pos| pos + 4)
}

fn header_value<'a>(headers: &'a str, name: &str) -> Option<&'a str> {
    headers.lines().find_map(|line| {
        let (key, value) = line.split_once(':')?;
        if key.eq_ignore_ascii_case(name) {
            Some(value.trim())
        } else {
            None
        }
    })
}

fn parse_http_origin(origin: &str) -> Result<(String, u16)> {
    let without_scheme = origin
        .strip_prefix("http://")
        .context("only http:// origins are supported in this spike")?;
    let authority = without_scheme.split('/').next().unwrap_or(without_scheme);
    let (host, port) = match authority.rsplit_once(':') {
        Some((host, port)) => (host, port.parse::<u16>()?),
        None => (authority, 80),
    };
    Ok((host.to_owned(), port))
}

fn parse_speed_request(first_line: &str) -> Option<u64> {
    let path = first_line.strip_prefix("GET ")?.split_whitespace().next()?;
    let query = path.strip_prefix("/speed?")?;
    for part in query.split('&') {
        let (key, value) = part.split_once('=')?;
        if key == "bytes" {
            return value.parse::<u64>().ok();
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    // A persisted secret key must round-trip: the second load returns the same identity,
    // which is what keeps the host's node ID stable across restarts.
    #[tokio::test]
    async fn secret_key_persists_and_round_trips() {
        let dir = std::env::temp_dir().join(format!("strmr-secret-test-{}", std::process::id()));
        tokio::fs::create_dir_all(&dir).await.unwrap();
        let path = dir.join("iroh_host_secret.key");
        let path_str = path.to_str().unwrap();

        let first = load_or_create_secret_key(path_str).await.unwrap();
        assert!(path.exists(), "key file should be created on first load");

        let second = load_or_create_secret_key(path_str).await.unwrap();
        assert_eq!(
            first.public(),
            second.public(),
            "reloading must yield the same node identity"
        );

        tokio::fs::remove_dir_all(&dir).await.ok();
    }

    // A corrupt key file is a hard error rather than silently minting a new identity.
    #[tokio::test]
    async fn secret_key_rejects_wrong_length() {
        let dir = std::env::temp_dir().join(format!("strmr-secret-bad-{}", std::process::id()));
        tokio::fs::create_dir_all(&dir).await.unwrap();
        let path = dir.join("bad.key");
        tokio::fs::write(&path, BASE64_URL_SAFE_NO_PAD.encode([0u8; 16]))
            .await
            .unwrap();

        assert!(load_or_create_secret_key(path.to_str().unwrap()).await.is_err());

        tokio::fs::remove_dir_all(&dir).await.ok();
    }
}
