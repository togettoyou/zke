import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, unwrap, unwrapEmpty } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  ListParams,
  ManagedUser,
  Pagination,
  RoleName,
  RoleBinding,
  ScopeType,
  UserStatus,
} from "../types";

export type UserListResult = { users: ManagedUser[]; pagination: Pagination };
export type RoleBindingListResult = { role_bindings: RoleBinding[]; pagination: Pagination };

type UserListParams = ListParams & { status?: UserStatus };
type RoleBindingListParams = ListParams & { role?: RoleName; scope_type?: ScopeType };

export function useUsers(params: UserListParams = {}, enabled = true) {
  return useQuery({
    queryKey: queryKeys.users(params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/users", { params: { query: params }, signal }),
      ) as UserListResult,
    enabled,
    placeholderData: (previous) => previous,
  });
}

export function useCreateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { username: string; displayName: string; password: string }) =>
      unwrap(
        await api.POST("/api/v1/users", {
          params: { header: csrfHeaders() },
          body: {
            username: input.username,
            display_name: input.displayName,
            password: input.password,
          },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.users });
    },
  });
}

export function useUpdateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string; displayName: string }) =>
      unwrap(
        await api.PUT("/api/v1/users/{user_id}", {
          params: { path: { user_id: input.userId }, header: csrfHeaders() },
          body: { display_name: input.displayName },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.users });
    },
  });
}

export function useSetUserStatus() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string; status: "active" | "disabled" }) =>
      unwrap(
        await api.PUT("/api/v1/users/{user_id}/status", {
          params: { path: { user_id: input.userId }, header: csrfHeaders() },
          body: { status: input.status, confirm: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.users });
    },
  });
}

export function useUnlockUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string }) =>
      unwrap(
        await api.POST("/api/v1/users/{user_id}/unlock", {
          params: { path: { user_id: input.userId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.users });
    },
  });
}

/** Resets another user's password; the Server revokes all their sessions. */
export function useResetUserPassword() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string; password: string }) =>
      unwrap(
        await api.POST("/api/v1/users/{user_id}/password-reset", {
          params: { path: { user_id: input.userId }, header: csrfHeaders() },
          body: { password: input.password, confirm: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.users });
    },
  });
}

export function useDeleteUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string }) =>
      unwrap(
        await api.DELETE("/api/v1/users/{user_id}", {
          params: { path: { user_id: input.userId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.users }),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roleBindings }),
      ]);
    },
  });
}

export function useRoleBindings(params: RoleBindingListParams = {}, enabled = true) {
  return useQuery({
    queryKey: queryKeys.roleBindings(params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/role-bindings", { params: { query: params }, signal }),
      ) as RoleBindingListResult,
    enabled,
    placeholderData: (previous) => previous,
  });
}

export function useCreateRoleBinding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      subjectId: string;
      role: RoleName;
      scopeType: ScopeType;
      tenantId?: string;
      projectId?: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/role-bindings", {
          params: { header: csrfHeaders() },
          body: {
            subject_id: input.subjectId,
            role: input.role,
            scope_type: input.scopeType,
            ...(input.tenantId ? { tenant_id: input.tenantId } : {}),
            ...(input.projectId ? { project_id: input.projectId } : {}),
            confirm: true,
          },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roleBindings });
    },
  });
}

export function useDeleteRoleBinding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { roleBindingId: string }) =>
      unwrapEmpty(
        await api.DELETE("/api/v1/role-bindings/{role_binding_id}", {
          params: { path: { role_binding_id: input.roleBindingId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.roleBindings });
    },
  });
}
