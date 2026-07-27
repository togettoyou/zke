import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import { api, csrfHeaders, unwrap, unwrapEmpty } from "../client";
import { queryKeys } from "../query-keys";
import type { CurrentSession } from "../types";

export function useSession(enabled = true): UseQueryResult<CurrentSession | null> {
  return useQuery({
    queryKey: queryKeys.session(),
    queryFn: async () => {
      const result = await api.GET("/api/v1/auth/me", {});
      // An expired or missing session is a normal answer here, not a failure.
      // Returning null replaces the cached identity; throwing would leave the
      // last successful `data` in place, and the shell would keep rendering the
      // desktop long after the Server started rejecting every request.
      if (result.response.status === 401) {
        return null;
      }
      return unwrap(result);
    },
    enabled,
    // A rejected session must surface immediately as "logged out" rather than
    // being retried behind a spinner.
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
  });
}

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { username: string; password: string }) =>
      unwrap(
        await api.POST("/api/v1/auth/login", {
          body: { username: input.username, password: input.password },
        }),
        { ignoreUnauthenticated: true },
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.session() });
    },
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      unwrapEmpty(await api.POST("/api/v1/auth/logout", { params: { header: csrfHeaders() } }), {
        ignoreUnauthenticated: true,
      }),
    onSettled: () => {
      // Server-side session data must never outlive the logout, even if the
      // request itself failed because the session had already expired.
      queryClient.clear();
    },
  });
}

export function useChangePassword() {
  return useMutation({
    mutationFn: async (input: { currentPassword: string; newPassword: string }) =>
      unwrapEmpty(
        await api.POST("/api/v1/auth/password", {
          params: { header: csrfHeaders() },
          body: {
            current_password: input.currentPassword,
            new_password: input.newPassword,
            confirm: true,
          },
        }),
      ),
  });
}

export function useHealth(enabled = true) {
  return useQuery({
    queryKey: queryKeys.health(),
    queryFn: async () => unwrap(await api.GET("/readyz", {})),
    enabled,
    retry: false,
    refetchInterval: 60_000,
  });
}
