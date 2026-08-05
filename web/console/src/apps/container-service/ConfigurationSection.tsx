import { useState } from "react";

import { useSessionContext } from "@/auth/session-context";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

import { ConfigMapSection } from "./ConfigMapSection";
import { SecretSection } from "./SecretSection";
import type { ClusterSectionProps } from "./types";

type ConfigurationSectionProps = ClusterSectionProps & {
  /** The Namespace every query and mutation in this section is scoped to. */
  namespace: string;
};

/**
 * ConfigMaps and Secrets, in one place and on two tabs.
 *
 * They are the same job — the configuration a workload is given — and different
 * enough that they are not one table: a Secret carries a type, its values are
 * masked until asked for, and reading or writing one takes its own permission
 * rather than the general cluster ones.
 *
 * The chosen section is rendered directly, with no element wrapped around it,
 * and the strip is handed to it as a control to place. Two reasons, and the
 * second one is the sharper: a switch left hanging above an open object would
 * offer to change a list that is not on screen; and a section that fills the
 * application view — the YAML editor is one — measures itself against its
 * parent, so an extra `flex-1` box between it and the frame collapses it to
 * nothing.
 *
 * The Secret tab is hidden without `cluster.secret.read`. That is not the
 * authorization — the Server checks every request independently — it is so the
 * page does not offer a tab whose every request comes back refused.
 */
export function ConfigurationSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
}: ConfigurationSectionProps) {
  const { permissions } = useSessionContext();
  const canReadSecrets = permissions.can("cluster.secret.read", {
    type: "project",
    tenantId,
    projectId,
  });
  const [tab, setTab] = useState<"configmaps" | "secrets">("configmaps");

  const shared = { clusterId, clusterName, namespace, tenantId, projectId };

  // Without the Secret permission there is one thing to show, and a strip with
  // a single tab is a control that cannot do anything.
  if (!canReadSecrets) {
    return <ConfigMapSection {...shared} />;
  }

  // A tab strip with no panes: the panes are the sections themselves, and each
  // one owns the whole view once it is chosen.
  const strip = (
    <Tabs value={tab} onValueChange={(value) => setTab(value as "configmaps" | "secrets")}>
      <TabsList className="mb-3 w-fit">
        <TabsTrigger value="configmaps">ConfigMap</TabsTrigger>
        <TabsTrigger value="secrets">Secret</TabsTrigger>
      </TabsList>
    </Tabs>
  );

  return tab === "configmaps" ? (
    <ConfigMapSection {...shared} tabs={strip} />
  ) : (
    <SecretSection {...shared} tabs={strip} />
  );
}
