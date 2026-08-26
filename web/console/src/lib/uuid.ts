/**
 * A random RFC 4122 v4 UUID, on plain HTTP as well as on HTTPS.
 *
 * `crypto.randomUUID` is declared `[SecureContext]`, so the browser defines it
 * only on HTTPS origins and on the `localhost` / `127.0.0.1` exceptions. Console
 * is opened over plain HTTP at a host address often enough — a private network,
 * an edge site, the Docker deployment someone reaches at `http://<host>:8080` —
 * that TLS cannot be a precondition for opening a window. Without this fallback
 * the call is `undefined` there, and the TypeError it throws inside a React
 * event handler takes the whole desktop down with it.
 *
 * `crypto.getRandomValues` predates the secure-context rule and is defined
 * everywhere, so the fallback keeps the same 122 bits of cryptographic
 * randomness. That is the point of using it rather than `Math.random` or a
 * timestamp: the idempotency keys in `api/client` are minted here too, and a
 * weaker source that repeats a key would make the Server answer a genuinely new
 * submission with an older result.
 */
export function randomUuid(): string {
  if (typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  // Version 4 in the high nibble of byte 6, variant 10 in the top bits of
  // byte 8. Both are fixed by RFC 4122; the remaining 122 bits stay random.
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
