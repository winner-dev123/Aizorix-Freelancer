//! Security engine (Phase 8/9 client side).
//!
//! Responsibilities:
//!   * Encrypt each screenshot with AES-256-GCM using the per-capture data key (DEK) the
//!     server returns (plaintext over mTLS). The server stores only the KMS-wrapped DEK, so a
//!     stolen bucket yields ciphertext only.
//!   * Hold an Ed25519 *device key* (generated on first run, stored in the OS keychain) and
//!     sign each screenshot's metadata so the backend can prove the capture came from this
//!     enrolled device and was not altered in transit (anti-tampering).
//!   * Compute SHA-256 over the ciphertext for end-to-end integrity verification.

use aes_gcm::aead::{Aead, KeyInit, Payload};
use aes_gcm::{Aes256Gcm, Nonce};
use ed25519_dalek::{Signer, SigningKey, VerifyingKey};
use rand::rngs::OsRng;
use rand::RngCore;
use sha2::{Digest, Sha256};
use zeroize::Zeroize;

use crate::config::KEYRING_SERVICE;
use crate::error::{AppError, Result};

/// Result of encrypting one screenshot.
pub struct Encrypted {
    pub ciphertext: Vec<u8>,
    pub nonce: [u8; 12],
    pub sha256_cipher: [u8; 32],
}

/// Encrypt `plaintext` (the compressed WebP bytes) with the server-provided 32-byte `dek`.
/// `aad` binds the ciphertext to its context (contract_id || captured_at) so it can't be
/// replayed under a different identity. The DEK is zeroized after use.
pub fn encrypt_screenshot(mut dek: Vec<u8>, plaintext: &[u8], aad: &[u8]) -> Result<Encrypted> {
    if dek.len() != 32 {
        return Err(AppError::Crypto("DEK must be 32 bytes".into()));
    }
    let cipher = Aes256Gcm::new_from_slice(&dek)
        .map_err(|e| AppError::Crypto(e.to_string()))?;

    let mut nonce_bytes = [0u8; 12];
    OsRng.fill_bytes(&mut nonce_bytes);
    let nonce = Nonce::from_slice(&nonce_bytes);

    let ciphertext = cipher
        .encrypt(nonce, Payload { msg: plaintext, aad })
        .map_err(|e| AppError::Crypto(format!("seal: {e}")))?;
    dek.zeroize();

    let mut hasher = Sha256::new();
    hasher.update(&ciphertext);
    let sha256_cipher: [u8; 32] = hasher.finalize().into();

    Ok(Encrypted { ciphertext, nonce: nonce_bytes, sha256_cipher })
}

/// The enrolled device identity. Persisted (private key) in the OS keychain; the public key is
/// registered with the backend (`devices.attestation_pubkey`) during login/enrollment.
pub struct DeviceKey {
    signing: SigningKey,
}

impl DeviceKey {
    /// Load the device key from the OS keychain, generating + persisting one on first run.
    pub fn load_or_create() -> Result<Self> {
        let entry = keyring::Entry::new(KEYRING_SERVICE, "device_signing_key")
            .map_err(|e| AppError::Storage(e.to_string()))?;
        match entry.get_password() {
            Ok(b64) => {
                let bytes = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, b64.as_bytes())
                    .map_err(|e| AppError::Crypto(e.to_string()))?;
                let arr: [u8; 32] = bytes.as_slice().try_into()
                    .map_err(|_| AppError::Crypto("bad device key length".into()))?;
                Ok(Self { signing: SigningKey::from_bytes(&arr) })
            }
            Err(_) => {
                let signing = SigningKey::generate(&mut OsRng);
                let b64 = base64::Engine::encode(
                    &base64::engine::general_purpose::STANDARD, signing.to_bytes());
                entry.set_password(&b64).map_err(|e| AppError::Storage(e.to_string()))?;
                Ok(Self { signing })
            }
        }
    }

    pub fn public_key_b64(&self) -> String {
        base64::Engine::encode(
            &base64::engine::general_purpose::STANDARD,
            self.signing.verifying_key().to_bytes())
    }

    /// Sign (sha256_cipher || gcm_nonce || captured_at_rfc3339 || contract_id) exactly as the
    /// backend re-derives it in ConfirmUpload. The 12-byte GCM nonce is bound into the signature
    /// so it is tamper-evident: without it, a wrong/garbage nonce would still verify yet make the
    /// blob permanently undecryptable. Returns the 64-byte signature.
    pub fn sign_metadata(&self, sha256_cipher: &[u8; 32], gcm_nonce: &[u8; 12], captured_at_rfc3339: &str, contract_id: &str) -> Vec<u8> {
        let mut msg = Vec::with_capacity(32 + 12 + captured_at_rfc3339.len() + contract_id.len());
        msg.extend_from_slice(sha256_cipher);
        msg.extend_from_slice(gcm_nonce);
        msg.extend_from_slice(captured_at_rfc3339.as_bytes());
        msg.extend_from_slice(contract_id.as_bytes());
        self.signing.sign(&msg).to_bytes().to_vec()
    }

    #[allow(dead_code)]
    pub fn verifying_key(&self) -> VerifyingKey { self.signing.verifying_key() }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encrypt_is_authenticated_and_hashed() {
        let dek = vec![7u8; 32];
        let enc = encrypt_screenshot(dek.clone(), b"webp-bytes", b"contract-1|2026").unwrap();
        assert_ne!(enc.ciphertext, b"webp-bytes");
        assert_eq!(enc.sha256_cipher.len(), 32);

        // Decrypt with the same key + AAD must succeed; wrong AAD must fail.
        let cipher = Aes256Gcm::new_from_slice(&dek).unwrap();
        let pt = cipher.decrypt(
            Nonce::from_slice(&enc.nonce),
            Payload { msg: &enc.ciphertext, aad: b"contract-1|2026" }).unwrap();
        assert_eq!(pt, b"webp-bytes");
        assert!(cipher.decrypt(
            Nonce::from_slice(&enc.nonce),
            Payload { msg: &enc.ciphertext, aad: b"wrong" }).is_err());
    }
}
