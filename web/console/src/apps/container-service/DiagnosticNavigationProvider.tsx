import { useMemo, useState, type ReactNode } from "react";

import {
  useAutoscalerDescribe,
  useGatewayDescribe,
  useIngressDescribe,
  usePersistentVolumeClaimDescribe,
  usePodDescribe,
  useServiceDescribe,
  useWorkloadDescribe,
} from "@/api/queries/describe";
import { AppShellContributionScope } from "@/apps/AppShell";

import { DescribeView } from "./DescribeView";
import { EventSection } from "./EventSection";
import { PodLogsView } from "./PodLogsView";
import {
  DiagnosticNavigationContextProvider,
  type DiagnosticDescribeTarget,
  type DiagnosticNavigationTarget,
} from "./diagnostic-navigation-context";
import { kindLabel } from "./workload-catalog";

type ResolvedTarget = DiagnosticNavigationTarget & { key: number };

/**
 * A small navigation stack for evidence opened from a diagnosis.
 *
 * The original section stays mounted behind the overlay. Returning therefore
 * restores its filters and already-read diagnosis snapshot — not where it was
 * scrolled to: the shell hands every view it opens the top of the work area.
 * Nested jumps (Ingress -> Service -> Pod logs) unwind one step at a time.
 */
export function DiagnosticNavigationProvider({
  clusterId,
  children,
}: {
  clusterId: string;
  children: ReactNode;
}) {
  const [stack, setStack] = useState<ResolvedTarget[]>([]);
  const active = stack.at(-1);
  const value = useMemo(
    () => ({
      open: (target: DiagnosticNavigationTarget) =>
        setStack((current) => [...current, { ...target, key: (current.at(-1)?.key ?? 0) + 1 }]),
    }),
    [],
  );
  const back = () => setStack((current) => current.slice(0, -1));

  return (
    <DiagnosticNavigationContextProvider value={value}>
      <AppShellContributionScope enabled={!active}>
        {/* Do not introduce a fixed-height layout box here. Long detail pages
            overflowed that box without increasing its flow height, so the
            AppShell's trailing spacer was laid out before the actual end of
            the detail. `contents` keeps the mounted view as the shell's layout
            child while `hidden` can still suppress it under an overlay. */}
        <div className={active ? "hidden" : "contents"}>{children}</div>
      </AppShellContributionScope>
      {active ? (
        <DiagnosticNavigationOverlay
          key={active.key}
          clusterId={clusterId}
          target={active}
          onBack={back}
        />
      ) : null}
    </DiagnosticNavigationContextProvider>
  );
}

function DiagnosticNavigationOverlay({
  clusterId,
  target,
  onBack,
}: {
  clusterId: string;
  target: ResolvedTarget;
  onBack: () => void;
}) {
  if (target.view === "events") {
    return (
      <EventSection
        clusterId={clusterId}
        namespace={target.namespace}
        initialObjectFilter={target.object}
        onBack={onBack}
      />
    );
  }
  if (target.view === "pod-logs") {
    return (
      <PodLogsView
        clusterId={clusterId}
        namespace={target.namespace}
        podName={target.pod.name}
        podUid={target.pod.uid}
        initialContainer={target.container}
        initialPrevious={target.previous}
        onBack={onBack}
      />
    );
  }
  return <DescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
}

function DescribeOverlay({
  clusterId,
  target,
  onBack,
}: {
  clusterId: string;
  target: DiagnosticDescribeTarget;
  onBack: () => void;
}) {
  switch (target.type) {
    case "pod":
      return <PodDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
    case "persistent-volume-claim":
      return <PVCDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
    case "service":
      return <ServiceDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
    case "ingress":
      return <IngressDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
    case "gateway":
      return <GatewayDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
    case "autoscaler":
      return <AutoscalerDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
    case "workload":
      return <WorkloadDescribeOverlay clusterId={clusterId} target={target} onBack={onBack} />;
  }
}

type OverlayProps<T extends DiagnosticDescribeTarget> = {
  clusterId: string;
  target: T;
  onBack: () => void;
};

function describeView(
  target: DiagnosticDescribeTarget,
  kind: string,
  query: {
    data: Parameters<typeof DescribeView>[0]["data"];
    isLoading: boolean;
    isFetching: boolean;
    error: unknown;
    refetch: () => unknown;
  },
  onBack: () => void,
) {
  return (
    <DescribeView
      name={target.name}
      kindLabel={kind}
      data={query.data}
      isLoading={query.isLoading}
      isFetching={query.isFetching}
      error={query.error}
      onRetry={() => void query.refetch()}
      onBack={onBack}
    />
  );
}

function PodDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "pod" }>>) {
  return describeView(
    target,
    "Pod",
    usePodDescribe(clusterId, target.namespace, target.name),
    onBack,
  );
}

function PVCDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "persistent-volume-claim" }>>) {
  return describeView(
    target,
    "PersistentVolumeClaim",
    usePersistentVolumeClaimDescribe(clusterId, target.namespace, target.name),
    onBack,
  );
}

function ServiceDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "service" }>>) {
  return describeView(
    target,
    "Service",
    useServiceDescribe(clusterId, target.namespace, target.name),
    onBack,
  );
}

function IngressDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "ingress" }>>) {
  return describeView(
    target,
    "Ingress",
    useIngressDescribe(clusterId, target.namespace, target.name),
    onBack,
  );
}

function GatewayDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "gateway" }>>) {
  return describeView(
    target,
    "Gateway",
    useGatewayDescribe(clusterId, target.namespace, target.name),
    onBack,
  );
}

function AutoscalerDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "autoscaler" }>>) {
  return describeView(
    target,
    "HorizontalPodAutoscaler",
    useAutoscalerDescribe(clusterId, target.namespace, target.name),
    onBack,
  );
}

function WorkloadDescribeOverlay({
  clusterId,
  target,
  onBack,
}: OverlayProps<Extract<DiagnosticDescribeTarget, { type: "workload" }>>) {
  return describeView(
    target,
    kindLabel(target.resource),
    useWorkloadDescribe(clusterId, target.namespace, target.resource, target.name),
    onBack,
  );
}
