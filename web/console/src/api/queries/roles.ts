import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, unwrap, unwrapEmpty } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type { ListParams, Pagination, PermissionDescriptor, Role } from "../types";

export type RoleListResult = { roles: Role[]; pagination: Pagination };
export type PermissionListResult = { permissions: PermissionDescriptor[] };

type RoleListParams = ListParams & { builtin?: "true" | "false" };

/**
 * Lists the platform's roles.
 *
 * Both kinds come back together, builtin first. They are the same thing to
 * whoever is reading — a named permission set someone can be bound to — and the
 * `builtin` flag is what the editor reads to decide the role is not theirs to
 * change.
 *
 * Requires `rbac.read`, like the bindings beside it.
 */
export function useRoles(params: RoleListParams = {}, enabled = true) {
  return useQuery({
    queryKey: queryKeys.roles(params),
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/roles", { params: { query: params }, signal })) as RoleListResult,
    enabled,
    placeholderData: (previous) => previous,
  });
}

/**
 * The permission vocabulary, with the caller's own ceiling attached.
 *
 * `held` is why this is fetched rather than hard-coded: a role may not carry a
 * permission its author does not hold globally, and the editor uses the flag to
 * disable those rather than letting an operator compose a role the Server will
 * refuse. The Server enforces it either way — this only decides what the form
 * offers.
 */
export function usePermissions(enabled = true) {
  return useQuery({
    queryKey: queryKeys.permissions(),
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/permissions", { signal })) as PermissionListResult,
    enabled,
    // The ceiling only moves when the caller's own bindings do, which ends the
    // session's assumptions anyway. Refetching it per mount buys nothing.
    staleTime: 5 * 60 * 1000,
  });
}

export function useCreateRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      name: string;
      displayName: string;
      description: string;
      permissions: string[];
    }) =>
      unwrap(
        await api.POST("/api/v1/roles", {
          params: { header: csrfHeaders() },
          body: {
            name: input.name,
            display_name: input.displayName,
            description: input.description,
            permissions: input.permissions,
            confirm: true,
          },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roles });
    },
  });
}

export function useUpdateRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      roleId: string;
      displayName: string;
      description: string;
      permissions: string[];
    }) =>
      unwrap(
        await api.PUT("/api/v1/roles/{role_id}", {
          params: { path: { role_id: input.roleId }, header: csrfHeaders() },
          body: {
            display_name: input.displayName,
            description: input.description,
            permissions: input.permissions,
            confirm: true,
          },
        }),
      ),
    onSuccess: async () => {
      // Editing a role changes what everyone bound to it can do, so the session's
      // own capabilities may have just changed too.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roles }),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roleBindings }),
      ]);
    },
  });
}

export function useDeleteRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { roleId: string }) =>
      unwrapEmpty(
        await api.DELETE("/api/v1/roles/{role_id}", {
          params: { path: { role_id: input.roleId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roles });
    },
  });
}
