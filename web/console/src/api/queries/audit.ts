import { useInfiniteQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";
import type { AuditEventPage } from "../types";

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

const AUDIT_PAGE_SIZE = 50;

/**
 * Audit events use cursor pagination, not `limit`/`offset`: the feed is
 * append-only and the Server scopes it to what `audit.read` allows.
 */
export function useAuditEvents(filters: AuditFilters = {}, enabled = true) {
  const query = Object.fromEntries(
    Object.entries(filters).filter(([, value]) => value !== undefined && value !== ""),
  );

  return useInfiniteQuery({
    queryKey: queryKeys.auditEvents(query),
    enabled,
    initialPageParam: "",
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET("/api/v1/audit-events", {
          params: {
            query: {
              ...query,
              limit: AUDIT_PAGE_SIZE,
              ...(pageParam ? { cursor: pageParam as string } : {}),
            },
          },
        }),
      ) as AuditEventPage,
    getNextPageParam: (lastPage) => (lastPage.next_cursor ? lastPage.next_cursor : undefined),
  });
}
