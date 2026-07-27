/**
 * Server timestamps are RFC 3339 with a UTC+8 offset. The browser renders them
 * in the viewer's own timezone, so the absolute value is always unambiguous and
 * the relative value is only ever a reading aid.
 */

const absoluteFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

const relativeFormatter = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });

export function parseTimestamp(value: string | null | undefined): Date | null {
  if (!value) {
    return null;
  }
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

export function formatAbsolute(value: string | Date | null | undefined): string {
  const date = value instanceof Date ? value : parseTimestamp(value);
  return date ? absoluteFormatter.format(date) : "—";
}

const RELATIVE_UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["year", 365 * 24 * 60 * 60 * 1000],
  ["month", 30 * 24 * 60 * 60 * 1000],
  ["day", 24 * 60 * 60 * 1000],
  ["hour", 60 * 60 * 1000],
  ["minute", 60 * 1000],
  ["second", 1000],
];

export function formatRelative(
  value: string | Date | null | undefined,
  now: Date = new Date(),
): string {
  const date = value instanceof Date ? value : parseTimestamp(value);
  if (!date) {
    return "—";
  }
  const deltaMs = date.getTime() - now.getTime();
  if (Math.abs(deltaMs) < 5_000) {
    return "刚刚";
  }
  for (const [unit, unitMs] of RELATIVE_UNITS) {
    if (Math.abs(deltaMs) >= unitMs || unit === "second") {
      return relativeFormatter.format(Math.round(deltaMs / unitMs), unit);
    }
  }
  return "刚刚";
}

/** Human readable duration for certificate lifetimes and lockout windows. */
export function formatDuration(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds <= 0) {
    return "已过期";
  }
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  if (days > 0) {
    return hours > 0 ? `${days} 天 ${hours} 小时` : `${days} 天`;
  }
  if (hours > 0) {
    return minutes > 0 ? `${hours} 小时 ${minutes} 分钟` : `${hours} 小时`;
  }
  if (minutes > 0) {
    return `${minutes} 分钟`;
  }
  return `${Math.floor(totalSeconds)} 秒`;
}
