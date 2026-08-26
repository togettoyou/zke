import createClient, { type Middleware } from "openapi-fetch";

import { CSRF_COOKIE_NAME, CSRF_HEADER_NAME, readCookie } from "@/lib/cookie";
import { randomUuid } from "@/lib/uuid";

import { ApiError, type ErrorBody } from "./errors";
import type { paths } from "./schema";

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);
const DEFAULT_TIMEOUT_MS = 30_000;
const LONG_REQUEST_TIMEOUT_MS = 5 * 60_000 + 10_000;

/**
 * Server-side double-submit CSRF: the `zke_csrf` cookie is readable by scripts
 * and must be echoed in `X-CSRF-Token` on every state-changing request. The
 * session cookie itself is HttpOnly and travels automatically because the
 * Console is served same-origin with the API.
 */
const csrfMiddleware: Middleware = {
  onRequest({ request }) {
    if (!SAFE_METHODS.has(request.method.toUpperCase())) {
      const token = readCookie(CSRF_COOKIE_NAME);
      if (token) {
        request.headers.set(CSRF_HEADER_NAME, token);
      }
    }
    return request;
  },
};

function timeoutFetch(timeoutMs: number) {
  return (input: Request): Promise<Response> =>
    fetch(input, {
      signal: AbortSignal.any([input.signal, AbortSignal.timeout(timeoutMs)]),
    });
}

function createApiClient(timeoutMs: number) {
  // Every request gets an upper bound so a hung connection cannot keep a
  // window's loading state forever. SSE does not use this client.
  //
  // The timeout is combined with the request's own signal rather than passed
  // alone: `fetch(input, { signal })` replaces whatever signal the Request
  // already carried, which is how the caller's cancellation — a closed window,
  // a superseded search — used to be dropped on the floor and every abandoned
  // request ran to completion anyway.
  const client = createClient<paths>({
    baseUrl,
    credentials: "same-origin",
    fetch: timeoutFetch(timeoutMs),
  });
  client.use(csrfMiddleware);
  return client;
}

// Requests are always same-origin. The origin is spelled out because `Request`
// requires an absolute URL outside the browser (unit tests run under Node).
const baseUrl = typeof location === "undefined" ? "http://localhost" : location.origin;

export const api = createApiClient(DEFAULT_TIMEOUT_MS);

/** Explicit client for bounded operations such as a first Cluster Terminal image pull. */
export const longRequestApi = createApiClient(LONG_REQUEST_TIMEOUT_MS);

type UnauthenticatedListener = () => void;

const unauthenticatedListeners = new Set<UnauthenticatedListener>();

/**
 * Notified whenever the Server rejects a request with 401 outside of the login
 * form, so the shell can drop local session state and return to the login view.
 */
export function onUnauthenticated(listener: UnauthenticatedListener): () => void {
  unauthenticatedListeners.add(listener);
  return () => unauthenticatedListeners.delete(listener);
}

function notifyUnauthenticated(): void {
  for (const listener of unauthenticatedListeners) {
    listener();
  }
}

/**
 * Every ZKE Server response is an envelope: `{ code, message, data }` on
 * success, and `{ code, message, data: { error_code, request_id } }` on failure.
 * The payload the Console actually wants is always one level down, in `data`.
 */
type Enveloped<T> = { data: T };

export type ApiResult<T> = {
  data?: Enveloped<T>;
  error?: unknown;
  response: Response;
};

type UnwrapOptions = {
  /** Login handles 401 itself; it must not trigger a global session reset. */
  ignoreUnauthenticated?: boolean;
};

function toApiError(response: Response, body: unknown): ApiError {
  const envelope = (body ?? {}) as Partial<ErrorBody>;
  // The stable machine-readable code and the request correlation id live inside
  // the envelope's `data`; the human-readable message sits alongside it.
  const detail = envelope.data ?? { error_code: "", request_id: "" };
  const retryAfterHeader = response.headers.get("Retry-After");
  const retryAfterSeconds = retryAfterHeader ? Number.parseInt(retryAfterHeader, 10) : null;
  return new ApiError({
    status: response.status,
    code: detail.error_code || String(response.status),
    message: envelope.message ?? response.statusText,
    requestId: detail.request_id ?? "",
    retryAfterSeconds: Number.isFinite(retryAfterSeconds) ? retryAfterSeconds : null,
  });
}

function raiseResultError(
  result: { error?: unknown; response: Response },
  options: UnwrapOptions,
): never {
  const error = toApiError(result.response, result.error);
  if (error.status === 401 && !options.ignoreUnauthenticated) {
    notifyUnauthenticated();
  }
  throw error;
}

/** Throws {@link ApiError} on failure, returns the unwrapped payload on success. */
export function unwrap<T>(result: ApiResult<T>, options: UnwrapOptions = {}): T {
  if (result.error !== undefined || !result.response.ok) {
    raiseResultError(result, options);
  }
  return result.data?.data as T;
}

/**
 * {@link unwrap} for the endpoints whose success payload is not the envelope.
 *
 * YAML is returned as `application/yaml` and read with `parseAs: "text"`, so the
 * body is the payload rather than one level down inside `data`. Failures are
 * still ordinary error envelopes and are raised identically.
 */
export function unwrapRaw<T>(
  result: { data?: T; error?: unknown; response: Response },
  options: UnwrapOptions = {},
): T {
  if (result.error !== undefined || !result.response.ok) {
    raiseResultError(result, options);
  }
  return result.data as T;
}

/**
 * Turns a failed raw response into the same {@link ApiError} the envelope path
 * raises, including the global session reset on 401.
 *
 * Streaming endpoints answer with `text/plain` rather than the JSON envelope, so
 * they cannot go through {@link unwrap} — but a failure before the stream starts
 * is still an ordinary error envelope, and must reach the operator worded and
 * handled exactly like every other one.
 */
export async function raiseResponseError(response: Response): Promise<never> {
  let body: unknown;
  try {
    body = await response.json();
  } catch {
    // A failure that is not an envelope: the status line is all there is.
    body = undefined;
  }
  const error = toApiError(response, body);
  if (error.status === 401) {
    notifyUnauthenticated();
  }
  throw error;
}

/**
 * Same as {@link unwrap} for endpoints whose payload carries no information.
 * These answer 200 with `data: null` rather than 204, so the envelope is still
 * parsed and a failure still raises.
 */
export function unwrapEmpty(
  result: { data?: unknown; error?: unknown; response: Response },
  options: UnwrapOptions = {},
): void {
  unwrap(result as ApiResult<unknown>, options);
}

/**
 * Idempotency keys protect creation endpoints against duplicated resources and
 * duplicated audit entries. The same key must be reused across retries of one
 * logical submission, so callers create it once per submission.
 */
export function newIdempotencyKey(): string {
  return randomUuid();
}

/**
 * The OpenAPI contract declares `X-CSRF-Token` as a required header parameter,
 * so call sites pass it explicitly and stay type-checked against the contract.
 * {@link csrfMiddleware} still refreshes it right before the request leaves, in
 * case the cookie rotated after the call was prepared.
 */
export function csrfHeaders(): { "X-CSRF-Token": string } {
  return { "X-CSRF-Token": readCookie(CSRF_COOKIE_NAME) ?? "" };
}

export function idempotentHeaders(idempotencyKey: string): {
  "X-CSRF-Token": string;
  "Idempotency-Key": string;
} {
  return { ...csrfHeaders(), "Idempotency-Key": idempotencyKey };
}
