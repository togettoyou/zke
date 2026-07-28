import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";
import type { AuditAction, AuditEvent, PageParams, Pagination } from "../types";

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
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/audit-events", { params: { query }, signal }),
      ) as AuditEventListResult,
    placeholderData: (previous) => previous,
  });
}

/**
 * The audit action vocabulary, so the Console can offer the exact values the
 * `action` filter matches on instead of asking an operator to type one.
 *
 * The list comes from the Server rather than a constant here: it is the Server
 * that writes these names, and a copy in the Console would be a second
 * definition with nothing keeping it in step. It is a closed set that only
 * changes when the Server does, so it is cached for the session.
 */
export function useAuditActions(enabled = true) {
  return useQuery({
    queryKey: queryKeys.auditActions(),
    enabled,
    staleTime: Infinity,
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/audit-events/actions", { signal })) as {
        audit_actions: AuditAction[];
      },
  });
}
