import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type {
  AgentEndpointProfile,
  AIModelSettings,
  AIModelSettingsUpdate,
  AIModelTestResult,
  PlatformSettings,
  PlatformSettingsUpdate,
} from "../types";
import { api, csrfHeaders, unwrap } from "../client";
import { queryKeys } from "../query-keys";

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
const aiModelSettingsKey = ["platform-ai-model"] as const;

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

/**
 * The AI model endpoint, which is platform configuration with its own revision.
 *
 * Its own query rather than a field of the settings object: the stored API Key
 * is never returned, so this section is read and saved on its own, and sharing
 * a revision with the Agent images would make two operators editing unrelated
 * things take each other's saves away.
 */
export function useAIModelSettings(enabled = true) {
  return useQuery({
    queryKey: aiModelSettingsKey,
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/platform/ai-model", { signal })) as AIModelSettings,
    enabled,
  });
}

export function useUpdateAIModelSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: AIModelSettingsUpdate) =>
      unwrap(
        await api.PUT("/api/v1/platform/ai-model", {
          params: { header: csrfHeaders() },
          body: input,
        }),
      ),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: aiModelSettingsKey }),
        // The endpoint is half of what makes AIOps available at all, so the
        // launcher's answer is no longer trustworthy once it changes.
        queryClient.invalidateQueries({ queryKey: queryKeys.aiTools() }),
      ]);
    },
  });
}

export function useSetAIModelEnabled() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { enabled: boolean; expectedRevision: number }) =>
      unwrap(
        await api.PATCH("/api/v1/platform/ai-model/enabled", {
          params: { header: csrfHeaders() },
          body: { enabled: input.enabled, expected_revision: input.expectedRevision },
        }),
      ) as AIModelSettings,
    onSuccess: async (settings) => {
      queryClient.setQueryData(aiModelSettingsKey, settings);
      // The launcher decides whether AIOps has an icon from the runtime's own
      // answer, which is cached for minutes. Without this the administrator
      // flips the switch and the desktop keeps showing the old state until
      // that cache expires.
      await queryClient.invalidateQueries({ queryKey: queryKeys.aiTools() });
    },
  });
}

/**
 * Tests what is stored, not what the form holds. A successful test has to be a
 * statement about the configuration that will run, so the answer is only
 * meaningful after a save.
 */
export function useTestAIModelSettings() {
  return useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/platform/ai-model/test", {
          params: { header: csrfHeaders() },
        }),
      ) as AIModelTestResult,
  });
}
