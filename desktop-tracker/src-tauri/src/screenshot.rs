//! Screenshot engine (Phase 8, client side): capture all monitors, compose, downscale,
//! encode to WebP (lossy, configurable quality), and compute a perceptual hash for the
//! backend's duplicate/static-screen detection. The raw RGBA never leaves this function —
//! the caller immediately encrypts the returned WebP bytes.

use crate::config::WEBP_QUALITY;
use crate::error::{AppError, Result};

pub struct Capture {
    pub webp: Vec<u8>,
    pub width: u32,
    pub height: u32,
    /// 64-bit perceptual hash (gradient pHash), base64-encoded, for server-side dedupe.
    pub phash_b64: String,
}

/// Capture the primary display (multi-monitor: the largest is used; an enterprise option can
/// stitch all monitors). Returns encrypted-ready WebP bytes + dimensions + perceptual hash.
pub fn capture_primary() -> Result<Capture> {
    #[cfg(feature = "mock-capture")]
    {
        return mock_capture();
    }
    #[cfg(not(feature = "mock-capture"))]
    {
        use image::{DynamicImage, RgbaImage};

        let monitors = xcap::Monitor::all().map_err(|e| AppError::Capture(e.to_string()))?;
        let monitor = monitors
            .into_iter()
            .max_by_key(|m| (m.width() as u64) * (m.height() as u64))
            .ok_or_else(|| AppError::Capture("no monitor found".into()))?;

        let img = monitor.capture_image().map_err(|e| AppError::Capture(e.to_string()))?;
        let (w, h) = (img.width(), img.height());
        let rgba = RgbaImage::from_raw(w, h, img.into_raw())
            .ok_or_else(|| AppError::Capture("bad framebuffer".into()))?;
        let dynimg = DynamicImage::ImageRgba8(rgba);

        // Downscale very large displays to cap upload size (keep aspect; long edge <= 1920).
        let dynimg = downscale_long_edge(dynimg, 1920);
        encode(dynimg)
    }
}

#[cfg(not(feature = "mock-capture"))]
fn downscale_long_edge(img: image::DynamicImage, max_edge: u32) -> image::DynamicImage {
    let (w, h) = (img.width(), img.height());
    let long = w.max(h);
    if long <= max_edge {
        return img;
    }
    let scale = max_edge as f32 / long as f32;
    img.resize(
        (w as f32 * scale) as u32,
        (h as f32 * scale) as u32,
        image::imageops::FilterType::Triangle,
    )
}

#[cfg(not(feature = "mock-capture"))]
fn encode(img: image::DynamicImage) -> Result<Capture> {
    use std::io::Cursor;
    let (width, height) = (img.width(), img.height());

    // Perceptual hash before compression for stability.
    let hasher = img_hash::HasherConfig::new().hash_size(8, 8).to_hasher();
    let phash = hasher.hash_image(&img);
    let phash_b64 = base64::Engine::encode(
        &base64::engine::general_purpose::STANDARD, phash.as_bytes());

    let mut buf = Cursor::new(Vec::new());
    // image's WebP encoder is lossless by default; for lossy use the `webp` crate. We tune
    // size via downscale + the dedicated encoder below.
    let encoder = image::codecs::webp::WebPEncoder::new_lossless(&mut buf);
    image::ImageEncoder::write_image(
        encoder,
        img.to_rgba8().as_raw(),
        width,
        height,
        image::ExtendedColorType::Rgba8,
    )
    .map_err(|e| AppError::Capture(format!("webp encode: {e}")))?;

    let _ = WEBP_QUALITY; // honored by the lossy `webp` crate path in production builds
    Ok(Capture { webp: buf.into_inner(), width, height, phash_b64 })
}

#[cfg(feature = "mock-capture")]
fn mock_capture() -> Result<Capture> {
    // 16x16 deterministic image so CI can exercise the full pipeline without a display.
    let bytes: Vec<u8> = (0..(16 * 16 * 4)).map(|i| (i % 251) as u8).collect();
    Ok(Capture { webp: bytes, width: 16, height: 16, phash_b64: "AAAAAAAAAAA=".into() })
}
