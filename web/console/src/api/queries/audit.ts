import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";
import type { AuditEvent, PageParams, Pagination } from "../types";

export type AuditFilters = {
  actor_type?: "user" | "agent" | "system";
  result?: "succeeded" | "failed" | "denied";
  action?: string;
  target_type?: string;
  request_id?: string;
  tenant_id?: string;
  project_id?: string;
  cluster_id?: string;
};

export type AuditEventListResult = { audit_events: AuditEvent[]; pagination: Pagination };

/** Audit events have no free-text search; only these filters are accepted. */
type AuditListParams = PageParams & AuditFilters;

/**
 * Audit events page the same way as every other list: `limit`/`offset` with a
 * total for the whole filtered set. The Server scopes the result to what the
 * caller's `audit.read` bindings allow, so the total already reflects
 * visibility rather than the raw table size.
 */
export function useAuditEvents(params: AuditListParams = {}, enabled = true) {
  const query = Object.fromEntries(
    Object.entries(params).filter(([, value]) => value !== undefined && value !== ""),
  );

  return useQuery({
    queryKey: queryKeys.auditEvents(query),
    enabled,
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/audit-events", { params: { query } })) as AuditEventListResult,
    placeholderData: (previous) => previous,
  });
}
