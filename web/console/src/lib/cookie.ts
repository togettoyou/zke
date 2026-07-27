/**
 * Reads a non-HttpOnly cookie.
 *
 * Only the CSRF cookie (`zke_csrf`) is readable from scripts; the session
 * cookie is HttpOnly and never visible here by design.
 */
export function readCookie(name: string): string | null {
  const prefix = `${name}=`;
  for (const entry of document.cookie.split("; ")) {
    if (entry.startsWith(prefix)) {
      return decodeURIComponent(entry.slice(prefix.length));
    }
  }
  return null;
}

export const CSRF_COOKIE_NAME = "zke_csrf";
export const CSRF_HEADER_NAME = "X-CSRF-Token";
