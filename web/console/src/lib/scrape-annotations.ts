/**
 * The annotation vocabulary the metrics collector discovers scrape targets by.
 *
 * Mirrors `pkg/shared/observability/names.go`. It is written out here rather
 * than generated because it is not part of any request or response shape: the
 * Console puts these on a Service the same way an operator would, and the thing
 * that reads them is vmagent inside the Cluster. Two screens need the same
 * words — the collection help and the networking form — so they share one list
 * instead of each spelling the keys out.
 */
export const SCRAPE_ANNOTATION_PREFIX = "zke-metrics-collector.io/";

export const SCRAPE_ANNOTATIONS = {
  scrape: `${SCRAPE_ANNOTATION_PREFIX}scrape`,
  scheme: `${SCRAPE_ANNOTATION_PREFIX}scheme`,
  path: `${SCRAPE_ANNOTATION_PREFIX}path`,
  port: `${SCRAPE_ANNOTATION_PREFIX}port`,
  auth: `${SCRAPE_ANNOTATION_PREFIX}auth`,
  insecureTLS: `${SCRAPE_ANNOTATION_PREFIX}tls-insecure-skip-verify`,
} as const;

/** The part after the shared prefix, which is what a table column shows. */
export function scrapeAnnotationName(key: string): string {
  return key.startsWith(SCRAPE_ANNOTATION_PREFIX)
    ? key.slice(SCRAPE_ANNOTATION_PREFIX.length)
    : key;
}

export type ScrapeAnnotationEntry = {
  key: string;
  purpose: string;
  values: string;
};

/** The whole vocabulary, in the order somebody reads it while filling a form. */
export const SCRAPE_ANNOTATION_GUIDE: ScrapeAnnotationEntry[] = [
  { key: SCRAPE_ANNOTATIONS.scrape, purpose: "开关", values: "true" },
  { key: SCRAPE_ANNOTATIONS.scheme, purpose: "协议", values: "http（默认）、https" },
  { key: SCRAPE_ANNOTATIONS.path, purpose: "指标路径", values: "/metrics（默认）" },
  { key: SCRAPE_ANNOTATIONS.port, purpose: "端口", values: "1-65535，留空用端点自身端口" },
  { key: SCRAPE_ANNOTATIONS.auth, purpose: "认证", values: "none（默认）、service-account" },
  { key: SCRAPE_ANNOTATIONS.insecureTLS, purpose: "跳过证书校验", values: "false（默认）、true" },
];

/**
 * What one click writes: an ordinary HTTP metrics endpoint.
 *
 * The switch plus the two values worth stating explicitly, and nothing else.
 * `port` is left out because an empty annotation means exactly what omitting it
 * means, so filling it in would only add a row that says nothing; `auth` and
 * `tls-insecure-skip-verify` are left out because their defaults are the safe
 * ones and neither should arrive on an object because a button was convenient.
 */
export const SCRAPE_ANNOTATION_QUICK_FILL: { key: string; value: string }[] = [
  { key: SCRAPE_ANNOTATIONS.scrape, value: "true" },
  { key: SCRAPE_ANNOTATIONS.scheme, value: "http" },
  { key: SCRAPE_ANNOTATIONS.path, value: "/metrics" },
];
