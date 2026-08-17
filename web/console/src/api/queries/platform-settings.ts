import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { AgentEndpointProfile, PlatformSettings, PlatformSettingsUpdate } from "../types";
import { api, csrfHeaders, unwrap } from "../client";

export type PlatformSettingsResult = {
  settings: PlatformSettings;
  agent_endpoint_profiles: AgentEndpointProfile[];
};

export type EndpointProfileInput = {
  name: string;
  registration_url: string;
  quic_address: string;
  registration_ca_certificate_pem: string;
  enabled: boolean;
};

const platformSettingsKey = ["platform-settings"] as const;

export function usePlatformSettings(enabled = true) {
  return useQuery({
    queryKey: platformSettingsKey,
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/platform/settings", { signal })) as PlatformSettingsResult,
    enabled,
  });
}

export function useReadyAgentEndpointProfiles(projectId: string | null) {
  return useQuery({
    queryKey: ["agent-endpoint-profiles", projectId] as const,
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/projects/{project_id}/agent-endpoint-profiles", {
          signal,
          params: { path: { project_id: projectId as string } },
        }),
      ),
    enabled: Boolean(projectId),
  });
}

/**
 * A partial save: the caller sends the workloads and fields its section owns,
 * and everything else keeps what the Server has. Sending the whole object would
 * mean every save carried values from sections the operator was not looking at.
 */
export function useUpdatePlatformSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: PlatformSettingsUpdate) =>
      unwrap(
        await api.PUT("/api/v1/platform/settings", {
          params: { header: csrfHeaders() },
          body: input,
        }),
      ),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: platformSettingsKey }),
  });
}

export function useCreateAgentEndpointProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: EndpointProfileInput) =>
      unwrap(
        await api.POST("/api/v1/platform/agent-endpoint-profiles", {
          params: { header: csrfHeaders() },
          body: input,
        }),
      ),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: platformSettingsKey }),
  });
}

export function useUpdateAgentEndpointProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: EndpointProfileInput & { id: string; expected_revision: number }) =>
      unwrap(
        await api.PUT("/api/v1/platform/agent-endpoint-profiles/{profile_id}", {
          params: { path: { profile_id: input.id }, header: csrfHeaders() },
          body: {
            name: input.name,
            registration_url: input.registration_url,
            quic_address: input.quic_address,
            registration_ca_certificate_pem: input.registration_ca_certificate_pem,
            enabled: input.enabled,
            expected_revision: input.expected_revision,
          },
        }),
      ),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: platformSettingsKey }),
  });
}

export function useDeleteAgentEndpointProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/api/v1/platform/agent-endpoint-profiles/{profile_id}", {
          params: { path: { profile_id: id }, header: csrfHeaders() },
        }),
      ),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: platformSettingsKey }),
  });
}
