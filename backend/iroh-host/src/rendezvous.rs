//! Connection-code rendezvous over the public BitTorrent mainline DHT (pkarr).
//!
//! A short connection code (e.g. `mshost-123456-123456`) carries no cryptographic
//! identity, so it cannot itself be an iroh invite. Instead we treat the code as a
//! *shared rendezvous secret*: both the host and the joining client deterministically
//! derive the same ed25519 keypair from the code, the host publishes a signed pkarr
//! record (containing the real iroh invite blob) under that derived public key, and the
//! client looks it up on the public DHT. No reachable backend URL is required on either
//! side — the public DHT is the resolver.
//!
//! SECURITY: the code's entropy is the entire security boundary. The derived public key is
//! itself published on the DHT, so a low-entropy code is open to an *offline* brute force
//! (derive a candidate key from each guess, compare against the queried public key). Anyone
//! who recovers the code can both read and *forge* the record, enabling a MITM. The mainline
//! DHT verifies signatures against the queried public key, so a record under the derived key
//! is authentic w.r.t. "someone who knew the code", but it does not protect against an
//! attacker who also knows the code.
//!
//! Mitigations in place:
//!   - High-entropy codes are the primary defense: the backend issues ~90-bit Crockford
//!     base32 codes (`generateToken` in services/remoteaccess/service.go), well beyond an
//!     offline brute force of the published derived key. This holds even if the key is
//!     observed for the full unclaimed window.
//!   - Single-use + expiring codes: enforced by the backend.
//!   - Claimed codes stop being published: the backend lists only *pending* codes in the
//!     rendezvous file. Mainline DHT nodes may retain the final mutable item, so resolvers
//!     explicitly reject records whose signed timestamp is older than the one-hour TTL.
//!     The backend separately requires a consumed, non-revoked device pairing on every
//!     proxied request. Already-paired clients reconnect via the host's stable iroh NodeID
//!     and never re-resolve the connection code.
//!
//! Residual risk: a first-contact MITM during the unclaimed window (an attacker who wins the
//! offline crack before the legitimate client claims — infeasible at ~90 bits, but the window
//! is bounded by the invite's expiry, not necessarily short). Closeable with a short
//! verification string (SAS) bound to the iroh channel keys, shown on both ends at pairing
//! time — the lighter equivalent of a full PAKE.

use std::net::IpAddr;

use anyhow::{anyhow, Context, Result};
use base64::prelude::*;
use iroh::{EndpointAddr, SecretKey, TransportAddr};
use iroh_dns::pkarr::{SignedPacket, Timestamp};
use mainline::{Dht, MutableItem};
use sha2::{Digest, Sha256};

/// Domain-separation prefix: keeps a code's derived key unique to this app and scheme
/// version. Bump the version suffix if the derivation or record format changes.
const KDF_DOMAIN: &str = "strmr-rendezvous-v1:";
/// DNS name the invite is published under inside the signed packet.
const TXT_NAME: &str = "_strmr";
/// Every host invite blob starts with this. A reassembled payload that doesn't is a corrupt
/// or foreign record under the derived key and must not be treated as a usable invite.
const INVITE_PREFIX: &str = "mshost-iroh-";
/// Max characters of invite payload per TXT chunk. DNS limits a TXT string to 255 bytes;
/// we reserve a few for the `NN:` ordering prefix and stay well under the limit.
const TXT_PAYLOAD_CHUNK: usize = 240;
/// Time-to-live for published records (seconds). The host republishes well before this.
pub const RENDEZVOUS_TTL_SECS: u32 = 60 * 60;
/// Small allowance for ordinary host/client wall-clock skew. Records beyond
/// this future window are invalid rather than being accepted indefinitely.
const RENDEZVOUS_CLOCK_SKEW_SECS: u64 = 5 * 60;

/// Deterministically derive an ed25519 secret key from a connection code.
///
/// Every 32-byte value is a valid ed25519 seed, so this is infallible. The SHA-256 of a
/// domain-separated code gives us those 32 bytes.
pub fn derive_secret_key(code: &str) -> SecretKey {
    let mut hasher = Sha256::new();
    hasher.update(KDF_DOMAIN.as_bytes());
    hasher.update(code.trim().as_bytes());
    let digest = hasher.finalize();
    let mut seed = [0u8; 32];
    seed.copy_from_slice(&digest[..32]);
    SecretKey::from_bytes(&seed)
}

/// The z-base-32 public key the record for `code` lives under (useful for logging).
pub fn derived_public_z32(code: &str) -> String {
    derive_secret_key(code).public().to_z32()
}

/// Build a signed pkarr packet for `code` carrying `invite`, split into ordered TXT chunks.
fn build_packet(code: &str, invite: &str) -> Result<SignedPacket> {
    let secret_key = derive_secret_key(code);
    let invite = invite.trim();
    if invite.is_empty() {
        return Err(anyhow!("invite is empty"));
    }

    let bytes = invite.as_bytes();
    let mut values: Vec<String> = Vec::new();
    for (idx, chunk) in bytes.chunks(TXT_PAYLOAD_CHUNK).enumerate() {
        if idx > 99 {
            return Err(anyhow!("invite too large to publish (>{} chunks)", 100));
        }
        // The invite blob is ASCII (prefix + base64url), so byte-chunking never splits a
        // multi-byte sequence. The `NN:` prefix makes reassembly order-independent.
        let payload = std::str::from_utf8(chunk).context("invite chunk is not utf8")?;
        values.push(format!("{idx:02}:{payload}"));
    }

    SignedPacket::from_txt_strings(&secret_key, TXT_NAME, values, RENDEZVOUS_TTL_SECS)
        .map_err(|err| anyhow!("build signed packet: {err}"))
}

/// Build the packet for `invite`, trimming its direct IP addresses when the full invite
/// doesn't fit a pkarr packet (1000 bytes). Hosts with many interfaces (Docker bridges,
/// IPv6, VPNs) exceed that. The node ID and relay URL are always kept: they're enough to
/// connect, and the client finds direct paths through the relay and address lookup.
/// IPs are added back best-first (public before private) while the packet still fits.
fn build_fitting_packet(code: &str, invite: &str) -> Result<SignedPacket> {
    let full_err = match build_packet(code, invite) {
        Ok(packet) => return Ok(packet),
        Err(err) => err,
    };
    let Some(mut addr) = decode_endpoint_addr(invite.trim()) else {
        return Err(full_err);
    };
    let mut ips: Vec<TransportAddr> = Vec::new();
    addr.addrs.retain(|a| match a {
        TransportAddr::Ip(_) => {
            ips.push(a.clone());
            false
        }
        _ => true,
    });
    ips.sort_by_key(|a| match a {
        TransportAddr::Ip(sock) => ip_rank(&sock.ip()),
        _ => u8::MAX,
    });
    let mut packet = build_packet(code, &encode_endpoint_addr(&addr)?)
        .context("invite too large even without direct addresses")?;
    for ip in ips {
        addr.addrs.insert(ip.clone());
        match build_packet(code, &encode_endpoint_addr(&addr)?) {
            Ok(fits) => packet = fits,
            Err(_) => {
                addr.addrs.remove(&ip);
            }
        }
    }
    Ok(packet)
}

/// Lower is better: public IPv4, public IPv6, private/ULA, link-local/loopback.
fn ip_rank(ip: &IpAddr) -> u8 {
    match ip {
        IpAddr::V4(v4) if v4.is_loopback() || v4.is_link_local() || v4.is_unspecified() => 3,
        IpAddr::V4(v4)
            if v4.is_private() || v4.octets()[0] == 100 && (v4.octets()[1] & 0xc0) == 64 =>
        {
            2
        }
        IpAddr::V4(_) => 0,
        IpAddr::V6(v6)
            if v6.is_loopback() || v6.is_unspecified() || (v6.segments()[0] & 0xffc0) == 0xfe80 =>
        {
            3
        }
        IpAddr::V6(v6) if (v6.segments()[0] & 0xfe00) == 0xfc00 => 2,
        IpAddr::V6(_) => 1,
    }
}

fn decode_endpoint_addr(invite: &str) -> Option<EndpointAddr> {
    let encoded = invite.strip_prefix(INVITE_PREFIX)?;
    let json = BASE64_URL_SAFE_NO_PAD.decode(encoded).ok()?;
    serde_json::from_slice(&json).ok()
}

fn encode_endpoint_addr(addr: &EndpointAddr) -> Result<String> {
    Ok(format!(
        "{INVITE_PREFIX}{}",
        BASE64_URL_SAFE_NO_PAD.encode(serde_json::to_vec(addr)?)
    ))
}

/// Reassemble the invite blob from a packet's ordered TXT chunks.
fn invite_from_packet(packet: &SignedPacket) -> Result<String> {
    let mut chunks: Vec<(usize, String)> = Vec::new();
    for record in packet.txt_records(TXT_NAME) {
        let (idx, payload) = record
            .split_once(':')
            .ok_or_else(|| anyhow!("malformed rendezvous chunk"))?;
        let idx: usize = idx.parse().context("malformed rendezvous chunk index")?;
        chunks.push((idx, payload.to_string()));
    }
    if chunks.is_empty() {
        return Err(anyhow!("no rendezvous chunks in packet"));
    }
    chunks.sort_by_key(|(idx, _)| *idx);
    // Guard against gaps/dupes from a partial or poisoned record.
    for (expected, (idx, _)) in chunks.iter().enumerate() {
        if *idx != expected {
            return Err(anyhow!("rendezvous chunks out of sequence"));
        }
    }
    let invite: String = chunks.into_iter().map(|(_, payload)| payload).collect();
    if !invite.starts_with(INVITE_PREFIX) {
        return Err(anyhow!("rendezvous record does not carry a valid invite"));
    }
    Ok(invite)
}

fn signed_packet_to_mutable_item(packet: &SignedPacket) -> MutableItem {
    MutableItem::new_signed_unchecked(
        *packet.public_key().as_bytes(),
        packet.signature().to_bytes(),
        packet.encoded_packet(),
        packet.timestamp().as_micros() as i64,
        None,
    )
}

fn mutable_item_to_signed_packet(item: &MutableItem) -> Result<SignedPacket> {
    SignedPacket::from_parts_unchecked(
        item.key(),
        item.signature(),
        Timestamp::from_micros(item.seq() as u64),
        item.value(),
    )
    .map_err(|err| anyhow!("decode signed packet: {err}"))
}

fn packet_is_fresh_at(packet: &SignedPacket, now_micros: u64) -> bool {
    let published_micros = packet.timestamp().as_micros();
    let skew_micros = RENDEZVOUS_CLOCK_SKEW_SECS * 1_000_000;
    if published_micros > now_micros.saturating_add(skew_micros) {
        return false;
    }
    let max_age_micros = u64::from(RENDEZVOUS_TTL_SECS) * 1_000_000;
    now_micros.saturating_sub(published_micros) <= max_age_micros
}

/// Publish (or refresh) the rendezvous record for `code` pointing at `invite`.
pub async fn publish(dht: &Dht, code: &str, invite: &str) -> Result<()> {
    let packet = build_fitting_packet(code, invite)?;
    let item = signed_packet_to_mutable_item(&packet);
    dht.clone()
        .as_async()
        .put_mutable(item, None)
        .await
        .map_err(|err| anyhow!("dht put failed: {err}"))?;
    Ok(())
}

/// Look up the rendezvous record for `code` and return the invite blob, if present.
///
/// The mainline DHT verifies the record's signature against the derived public key, so a
/// returned value is guaranteed to have been signed by someone who knew the code.
pub async fn resolve(dht: &Dht, code: &str) -> Result<Option<String>> {
    let public_key = derive_secret_key(code).public();
    let item = dht
        .clone()
        .as_async()
        .get_mutable_most_recent(public_key.as_bytes(), None)
        .await;
    let Some(item) = item else {
        return Ok(None);
    };
    let packet = mutable_item_to_signed_packet(&item)?;
    if !packet_is_fresh_at(&packet, Timestamp::now().as_micros()) {
        return Ok(None);
    }
    let invite = invite_from_packet(&packet)?;
    Ok(Some(invite))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn derive_is_deterministic_and_domain_separated() {
        let a = derive_secret_key("mshost-111111-222222");
        let b = derive_secret_key("mshost-111111-222222");
        let c = derive_secret_key("mshost-111111-222223");
        assert_eq!(a.public(), b.public());
        assert_ne!(a.public(), c.public());
    }

    #[test]
    fn derive_trims_whitespace() {
        assert_eq!(
            derive_secret_key("mshost-111111-222222").public(),
            derive_secret_key("  mshost-111111-222222\n").public()
        );
    }

    #[test]
    fn packet_round_trips_invite() {
        let code = "mshost-424242-131313";
        // Larger than one TXT chunk to exercise the multi-chunk path.
        let invite = format!("mshost-iroh-{}", "A".repeat(600));
        let packet = build_packet(code, &invite).expect("build");
        // Record must live under the key the resolver will query.
        assert_eq!(packet.public_key(), derive_secret_key(code).public());
        let recovered = invite_from_packet(&packet).expect("reassemble");
        assert_eq!(recovered, invite);
    }

    #[test]
    fn oversized_invite_is_trimmed_to_fit_keeping_relay_and_public_ips() {
        let code = "mshost-424242-131313";
        let id = SecretKey::from_bytes(&[7u8; 32]).public();
        let mut addrs = std::collections::BTreeSet::new();
        addrs.insert(TransportAddr::Relay(
            "https://use1-1.relay.n0.iroh-canary.iroh.link./"
                .parse()
                .unwrap(),
        ));
        addrs.insert(TransportAddr::Ip("203.0.113.9:41064".parse().unwrap()));
        for i in 0..40u16 {
            addrs.insert(TransportAddr::Ip(
                format!("172.17.{i}.1:41064").parse().unwrap(),
            ));
            addrs.insert(TransportAddr::Ip(
                format!("[fd00::{i:x}]:41064").parse().unwrap(),
            ));
        }
        let invite = encode_endpoint_addr(&EndpointAddr { id, addrs }).unwrap();
        assert!(
            build_packet(code, &invite).is_err(),
            "fixture must exceed the packet limit"
        );

        let packet = build_fitting_packet(code, &invite).expect("trimmed packet");
        let trimmed = decode_endpoint_addr(&invite_from_packet(&packet).unwrap()).unwrap();
        assert_eq!(trimmed.id, id);
        assert!(trimmed
            .addrs
            .iter()
            .any(|a| matches!(a, TransportAddr::Relay(_))));
        assert!(trimmed
            .addrs
            .contains(&TransportAddr::Ip("203.0.113.9:41064".parse().unwrap())));
    }

    #[test]
    fn empty_invite_is_rejected() {
        assert!(build_packet("mshost-1-2", "   ").is_err());
    }

    #[test]
    fn record_without_invite_prefix_is_rejected() {
        // A corrupt or foreign record under the derived key reassembles to a non-invite
        // payload; resolution must reject it rather than return junk that later fails
        // `decode_invite` with "invite must start with mshost-iroh-".
        let packet = build_packet("mshost-424242-131313", "not-a-real-invite-blob").expect("build");
        assert!(invite_from_packet(&packet).is_err());
    }

    #[test]
    fn stale_or_implausibly_future_packets_are_rejected() {
        let packet = build_packet("mshost-424242-131313", "mshost-iroh-test").expect("build");
        let published = packet.timestamp().as_micros();
        let ttl_micros = u64::from(RENDEZVOUS_TTL_SECS) * 1_000_000;
        let skew_micros = RENDEZVOUS_CLOCK_SKEW_SECS * 1_000_000;

        assert!(packet_is_fresh_at(&packet, published + ttl_micros));
        assert!(!packet_is_fresh_at(&packet, published + ttl_micros + 1));
        assert!(!packet_is_fresh_at(
            &packet,
            published.saturating_sub(skew_micros + 1)
        ));
    }
}
