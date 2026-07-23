import * as ExpoCrypto from "expo-crypto";

// Hermes ships no WebCrypto. Without it supabase-js silently downgrades the
// PKCE code challenge to `plain` (it warns: "WebCrypto API is not supported.
// Code challenge method will default to use plain instead of sha256.") — the
// verifier travels to the auth server in clear text as the challenge.
//
// supabase-js needs exactly three globals to pick s256 (auth-js
// `generatePKCEChallenge`): `crypto.getRandomValues`, `crypto.subtle.digest`
// and `TextEncoder`. expo-crypto provides the first two natively; the third is
// a few lines of UTF-8 encoding. This module MUST be imported before the
// Supabase client is constructed — see the first line of `lib/supabase.ts`.

const globals = globalThis as Record<string, any>;

// Minimal UTF-8 encoder. Only `encode()` is used by auth-js; the full
// TextEncoder surface (encodeInto, streams) is deliberately not implemented.
if (typeof globals.TextEncoder === "undefined") {
  globals.TextEncoder = class TextEncoder {
    readonly encoding = "utf-8";

    encode(input = ""): Uint8Array {
      const bytes: number[] = [];
      for (let i = 0; i < input.length; i++) {
        let code = input.charCodeAt(i);
        // Recombine surrogate pairs into a single code point.
        if (code >= 0xd800 && code <= 0xdbff && i + 1 < input.length) {
          const next = input.charCodeAt(i + 1);
          if (next >= 0xdc00 && next <= 0xdfff) {
            code = (code - 0xd800) * 0x400 + (next - 0xdc00) + 0x10000;
            i++;
          }
        }
        if (code < 0x80) {
          bytes.push(code);
        } else if (code < 0x800) {
          bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
        } else if (code < 0x10000) {
          bytes.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
        } else {
          bytes.push(
            0xf0 | (code >> 18),
            0x80 | ((code >> 12) & 0x3f),
            0x80 | ((code >> 6) & 0x3f),
            0x80 | (code & 0x3f)
          );
        }
      }
      return new Uint8Array(bytes);
    }
  };
}

type DigestAlgorithm = string | { name: string };

function normalizeAlgorithm(algorithm: DigestAlgorithm): ExpoCrypto.CryptoDigestAlgorithm {
  const name = (typeof algorithm === "string" ? algorithm : algorithm?.name) ?? "";
  // WebCrypto spells these "SHA-256"; expo-crypto uses the same strings.
  const normalized = name.toUpperCase();
  switch (normalized) {
    case "SHA-1":
      return ExpoCrypto.CryptoDigestAlgorithm.SHA1;
    case "SHA-256":
      return ExpoCrypto.CryptoDigestAlgorithm.SHA256;
    case "SHA-384":
      return ExpoCrypto.CryptoDigestAlgorithm.SHA384;
    case "SHA-512":
      return ExpoCrypto.CryptoDigestAlgorithm.SHA512;
    default:
      throw new Error(`Unsupported digest algorithm: ${name}`);
  }
}

const existing = globals.crypto ?? {};

if (typeof existing.getRandomValues !== "function") {
  existing.getRandomValues = ExpoCrypto.getRandomValues;
}

if (typeof existing.subtle?.digest !== "function") {
  existing.subtle = {
    ...(existing.subtle ?? {}),
    digest: (algorithm: DigestAlgorithm, data: BufferSource) =>
      ExpoCrypto.digest(normalizeAlgorithm(algorithm), data),
  };
}

// `crypto` may already exist as a non-writable global, so assign through
// defineProperty and fall back to a plain assignment.
try {
  Object.defineProperty(globals, "crypto", {
    value: existing,
    configurable: true,
    writable: true,
  });
} catch {
  globals.crypto = existing;
}

/**
 * True when supabase-js will use s256 rather than falling back to `plain`.
 * Mirrors the exact check in auth-js `generatePKCEChallenge`. Logged at login
 * in __DEV__ so a regression here is visible instead of silent.
 */
export function hasWebCryptoSupport(): boolean {
  return (
    typeof globals.crypto !== "undefined" &&
    typeof globals.crypto.subtle !== "undefined" &&
    typeof globals.TextEncoder !== "undefined"
  );
}
