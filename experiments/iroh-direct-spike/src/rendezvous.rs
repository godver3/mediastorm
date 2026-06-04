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
//! SECURITY: the code's entropy is the entire security boundary. Anyone who knows (or
//! brute-forces) the code can derive the same secret key and therefore both read and
//! *forge* the record, enabling a MITM. The mainline DHT verifies signatures against the
//! queried public key, so a record under the derived key is authentic w.r.t. "someone who
//! knew the code", but it does not protect against an attacker who also knows the code.
//! Mitigate with: single-use + short-lived codes (already enforced by the backend), higher
//! code entropy, and ideally a PAKE (SPAKE2-style) handshake layered on the iroh
//! connection. See docs/TODO.md.

use anyhow::{anyhow, Context, Result};
use iroh::SecretKey;
use iroh_dns::pkarr::{SignedPacket, Timestamp};
use mainline::{Dht, MutableItem};
use sha2::{Digest, Sha256};

/// Domain-separation prefix: keeps a code's derived key unique to this app and scheme
/// version. Bump the version suffix if the derivation or record format changes.
const KDF_DOMAIN: &str = "strmr-rendezvous-v1:";
/// DNS name the invite is published under inside the signed packet.
const TXT_NAME: &str = "_strmr";
/// Max characters of invite payload per TXT chunk. DNS limits a TXT string to 255 bytes;
/// we reserve a few for the `NN:` ordering prefix and stay well under the limit.
const TXT_PAYLOAD_CHUNK: usize = 240;
/// Time-to-live for published records (seconds). The host republishes well before this.
pub const RENDEZVOUS_TTL_SECS: u32 = 60 * 60;

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
    Ok(chunks.into_iter().map(|(_, payload)| payload).collect())
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

/// Publish (or refresh) the rendezvous record for `code` pointing at `invite`.
pub async fn publish(dht: &Dht, code: &str, invite: &str) -> Result<()> {
    let packet = build_packet(code, invite)?;
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
    fn empty_invite_is_rejected() {
        assert!(build_packet("mshost-1-2", "   ").is_err());
    }
}
