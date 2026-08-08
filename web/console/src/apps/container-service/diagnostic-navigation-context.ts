import { createContext, useContext } from "react";

import type { KubernetesWorkloadResource } from "@/api/types";

export type DiagnosticDescribeTarget =
  | { type: "pod"; namespace: string; name: string }
  | { type: "persistent-volume-claim"; namespace: string; name: string }
  | { type: "service"; namespace: string; name: string }
  | { type: "ingress"; namespace: string; name: string }
  | { type: "gateway"; namespace: string; name: string }
  | { type: "autoscaler"; namespace: string; name: string }
  | {
      type: "workload";
      namespace: string;
      resource: KubernetesWorkloadResource;
      name: string;
    };

export type DiagnosticNavigationTarget =
  | {
      view: "events";
      namespace: string;
      object: { kind: string; name: string; uid: string };
    }
  | {
      view: "pod-logs";
      namespace: string;
      pod: { name: string; uid: string };
      container?: string;
      previous?: boolean;
    }
  | ({ view: "describe" } & DiagnosticDescribeTarget);

type DiagnosticNavigationValue = {
  open: (target: DiagnosticNavigationTarget) => void;
};

const DiagnosticNavigationContext = createContext<DiagnosticNavigationValue | null>(null);

export const DiagnosticNavigationContextProvider = DiagnosticNavigationContext.Provider;

/**
 * Opens supporting evidence without destroying the diagnosis being read.
 *
 * This is navigation state only. It deliberately carries no permission flags:
 * entry points hide actions the caller cannot use, while every destination API
 * still performs the authoritative Server-side permission check.
 */
export function useDiagnosticNavigation(): DiagnosticNavigationValue | null {
  return useContext(DiagnosticNavigationContext);
}
