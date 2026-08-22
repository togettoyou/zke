// Package aiskills is the playbook library AIOps offers the model.
//
// A skill is a procedure, not a capability. It says which of the Server's
// existing tools to reach for, in what order, and what conclusion the evidence
// supports — and it cannot add a tool, widen a permission, change an approval
// mode or address a second Cluster, because none of those live here. That
// separation is the whole design: the tool catalogue carries the security
// boundary and stays Server-owned and closed, while the part a deployment
// might genuinely want to tune is a document with no authority of its own.
//
// The library ships with the Server rather than being authored in a session.
// Skill bodies are instructions the model is meant to follow, so a skill an
// operator or a cluster could write would be exactly the prompt injection the
// rest of Phase 4 is built to refuse. Tenant-authored playbooks are a later
// question and would need their own trust marking; this package answers the
// first one, which is whether a procedure ZKE itself vouches for makes the
// agent better at the investigations it already does.
package aiskills

import "github.com/togettoyou/zke/pkg/server/airuntime"

// Library is the shipped catalogue.
//
// A value rather than a package-level slice so a test can hold a different one,
// and so the composition in server.go reads like every other dependency.
type Library struct {
	skills []airuntime.Skill
}

func New() *Library { return &Library{skills: builtin()} }

func (library *Library) Skills() []airuntime.Skill { return library.skills }

// builtin is the shipped set.
//
// Every skill names the tools it actually directs the model to call. The
// runtime drops a skill whose tools this deployment did not compose — a
// playbook whose third step is a metrics query is worse than no playbook at
// all in an installation that has no metrics, because the model plans around
// the step and then discovers it cannot take it.
func builtin() []airuntime.Skill {
	return []airuntime.Skill{
		{
			ID:      "pod-crashloop",
			Title:   "Pod 反复重启与启动失败",
			Summary: "Pod 处于 CrashLoopBackOff、Error、ImagePullBackOff 或反复重启时的取证顺序。",
			Tools: []string{
				"list_resources", "describe_resource", "get_resource", "get_pod_logs",
			},
			Body: `# Pod 反复重启与启动失败

## 何时使用
Pod 长期不进入 Running、进入 CrashLoopBackOff/Error/ImagePullBackOff，或 restartCount 持续增长。

## 取证顺序
1. list_resources 列出目标 Namespace 的 Pod，确认异常范围：单个 Pod、同一工作负载的全部副本，还是整个 Namespace。
   范围决定后面查什么——单副本异常多半是节点或数据问题，全部副本异常多半是镜像、配置或依赖问题。
2. describe_resource 读该 Pod。先看容器的 state 与 lastState：
   - waiting.reason=ImagePullBackOff/ErrImagePull：镜像名、tag 或拉取凭证问题，去看 Event 里的具体拉取错误；
   - lastState.terminated.reason=OOMKilled：内存不足，读 get_resource 确认 resources.limits.memory；
   - lastState.terminated.exitCode 非 0 且非 137：应用自身退出，去读日志；
   - waiting.reason=CreateContainerConfigError：引用了不存在的 ConfigMap/Secret 键。
3. get_pod_logs 读上一个已终止实例的日志（previous=true）。当前实例的日志是重启之后的，看不到崩溃原因；
   崩溃原因通常写在上一实例日志的末尾。容器多于一个时必须指定 container。
4. 需要确认探针、启动命令、镜像、环境变量或挂载时，get_resource 读 Pod 完整定义。
5. 怀疑是探针把健康容器杀掉时，比较 livenessProbe 的 initialDelaySeconds/timeoutSeconds 与日志里应用真实的启动耗时。

## 结论要求
写清是哪一类原因（镜像、配置、资源、探针、应用自身），依据是哪一条 Event、哪一段日志或哪一个字段，
以及下一步应该改什么对象的什么字段。不要在只看到 CrashLoopBackOff 这一个事实时就断言原因。`,
		},
		{
			ID:      "workload-rollout",
			Title:   "工作负载副本不就绪与滚动更新卡住",
			Summary: "Deployment/StatefulSet/DaemonSet 副本不足、滚动更新停滞或新版本不可用时的排查路径。",
			Tools: []string{
				"describe_resource", "get_resource", "list_resources", "list_workload_revisions",
			},
			Body: `# 工作负载副本不就绪与滚动更新卡住

## 何时使用
readyReplicas 少于 replicas、更新长时间停在中间状态，或新副本起不来而旧副本仍在服务。

## 取证顺序
1. describe_resource 读工作负载本身，看 conditions：
   - Progressing=False 且 reason=ProgressDeadlineExceeded：新副本始终没有就绪，问题在新副本上；
   - Available=False：可用副本数低于要求，先确认是新副本起不来还是旧副本被删多了；
   - ReplicaFailure=True：创建副本被拒绝，原因通常是配额、PodSecurity 或准入控制。
2. get_resource 读 spec.strategy 与 spec.replicas，确认 maxUnavailable/maxSurge 是否让更新根本无法推进
   （例如 maxSurge=0 且集群没有空余资源）。
3. list_resources 按标签选择器列出该工作负载的 Pod，挑一个新版本的 Pod 继续排查；
   Pod 本身不就绪时改用「Pod 反复重启与启动失败」的顺序。
4. 新旧版本差异不明时，list_workload_revisions 读历史版本，确认这次更新改了什么。
5. StatefulSet 卡在某个序号时，注意它是有序更新：前一个 Pod 不就绪，后面的不会开始。

## 结论要求
说明卡在哪一环（调度、镜像、启动、探针、PDB、配额），并给出可执行的下一步。
需要回滚时先 list_workload_revisions 确认目标 revision，再走预检与审批，不要直接断言应该回滚到某个版本。`,
		},
		{
			ID:      "scheduling-pressure",
			Title:   "Pod Pending 与节点资源压力",
			Summary: "Pod 长期 Pending、调度失败或节点资源紧张时，如何区分是集群容量还是单个对象的约束。",
			Tools: []string{
				"describe_resource", "list_nodes", "cluster_overview", "get_resource",
			},
			Body: `# Pod Pending 与节点资源压力

## 何时使用
Pod 停在 Pending、Event 里出现 FailedScheduling，或怀疑节点资源不足、节点异常。

## 取证顺序
1. describe_resource 读该 Pod，FailedScheduling 的 message 已经说明了每个节点被排除的原因，
   例如 "Insufficient cpu"、"node(s) had untolerated taint"、"didn't match Pod's node affinity"。先读它再猜。
2. 依据 message 分支：
   - Insufficient cpu/memory：list_nodes 看容量与可分配量，判断是整体不足还是碎片化；
   - untolerated taint：get_resource 读 Pod 的 tolerations，list_nodes 读节点污点；
   - node affinity/selector 不匹配：比对 Pod 的 nodeSelector/affinity 与节点标签；
   - volume node affinity conflict：卷绑定在某个可用区，转「PVC Pending 与卷挂载失败」；
   - 没有任何候选节点：list_nodes 确认是否所有节点都 NotReady 或不可调度。
3. cluster_overview 判断这是个别对象的约束还是集群级压力：多个 Namespace 同时出现 Pending 才是容量问题。
4. 节点侧异常时读节点 conditions：MemoryPressure、DiskPressure、PIDPressure 与 Ready=False 是不同的处置方向。

## 结论要求
明确区分「集群容量不足」与「这个对象的约束条件没有节点满足」。前者的下一步是扩容或释放，
后者的下一步是修改对象的 request、selector、affinity 或 toleration。不要把单个 Pod 的选择器写错说成集群没有资源。`,
		},
		{
			ID:      "service-routing",
			Title:   "Service 与 Ingress 访问不通",
			Summary: "从后往前逐段验证：Endpoints、Service 选择器、端口映射与入口规则。",
			Tools:   []string{"get_resource", "list_resources", "describe_resource"},
			Body: `# Service 与 Ingress 访问不通

## 何时使用
外部访问 404/502/503、集群内访问某个 Service 不通，或怀疑流量没有到达期望的 Pod。

## 取证顺序（从后往前，比从前往后收敛快）
1. get_resource 读 Service，记下 selector、ports 与 targetPort、type。
2. list_resources 读同名 Endpoints（v1/Endpoints）或 discovery.k8s.io/v1/EndpointSlice。
   没有任何地址就说明 Service 选不到就绪 Pod，问题在第 3 步；有地址则问题在入口或端口。
3. 没有后端时：用 Service 的 selector 作为 label_selector 去 list_resources 列 Pod。
   - 一个都没有：selector 与 Pod 标签不匹配；
   - 有 Pod 但不在 Endpoints 里：Pod 未就绪，转「Pod 反复重启与启动失败」或看 readinessProbe。
4. 有后端但仍不通时：核对 targetPort 与容器 containerPort/名称是否一致，协议（TCP/UDP）是否一致。
5. 入口侧：get_resource 读 Ingress 或 Gateway/HTTPRoute，核对 host、path、pathType、backend 指向的 Service 名与端口，
   以及 ingressClassName/parentRefs 是否指向真实存在且就绪的控制器。describe_resource 读入口对象的 Event 与 status。
6. 怀疑被网络策略拦截时，list_resources 读该 Namespace 的 NetworkPolicy，确认是否存在默认拒绝且没有放行本次流量。

## 结论要求
指明断在哪一段（入口规则、Service 选择器、端口映射、Pod 就绪、网络策略），并给出该段上要改的对象和字段。
AIOps 不能发起网络请求，不要声称自己访问过某个地址。`,
		},
		{
			ID:      "storage-binding",
			Title:   "PVC Pending 与卷挂载失败",
			Summary: "PVC 长期 Pending、Pod 卡在 ContainerCreating 或卷挂载报错时的判定顺序。",
			Tools:   []string{"describe_resource", "get_resource", "list_resources"},
			Body: `# PVC Pending 与卷挂载失败

## 何时使用
PVC 停在 Pending、Pod 停在 ContainerCreating，或 Event 里出现 FailedMount、FailedAttachVolume。

## 取证顺序
1. describe_resource 读 PVC，Event 通常直接给出原因：
   - ProvisioningFailed：CSI 驱动或后端存储的问题，读 message 里的驱动返回；
   - WaitForFirstConsumer：StorageClass 是延迟绑定，PVC 会一直 Pending 直到 Pod 被调度，这不是故障；
   - 没有任何 Event 且 storageClassName 为空：集群没有默认 StorageClass。
2. get_resource 读 PVC 的 spec：accessModes、resources.requests.storage、storageClassName、volumeMode。
3. list_resources 读 StorageClass，确认引用的类存在、provisioner 正确、volumeBindingMode 是否为 WaitForFirstConsumer。
4. 静态供给场景下列出 PV，确认是否存在容量、accessModes 与 selector 都匹配且处于 Available 的卷。
5. 挂载失败（PVC 已 Bound 但 Pod 起不来）时：describe_resource 读 Pod 的 Event。
   - Multi-Attach error：卷是 ReadWriteOnce 却被两个节点上的 Pod 同时要求，常见于滚动更新；
   - FailedMount + timeout：节点上的 CSI 组件或后端不可达。

## 结论要求
区分「还没有供给」「供给失败」「已绑定但挂载失败」三种状态，它们的下一步完全不同。
WaitForFirstConsumer 造成的 Pending 要说明它是预期行为，并转去看 Pod 为什么没有被调度。`,
		},
		{
			ID:      "resource-saturation",
			Title:   "资源用量与饱和度评估",
			Summary: "用指标目录判断集群、节点、Namespace 或工作负载是否真的资源不足，而不是只看一次快照。",
			Tools: []string{
				"list_metric_queries", "query_metrics", "cluster_overview", "list_nodes",
			},
			Body: `# 资源用量与饱和度评估

## 何时使用
需要回答「是不是资源不够」「哪个 Namespace/工作负载吃掉了资源」「这次异常前后用量有没有变化」。

## 取证顺序
1. list_metric_queries 先看目录。ZKE 不接受任意 PromQL，只能调用目录里的查询；
   目录同时说明每个查询支持哪些参数（namespace、top、回看窗口）。不要凭记忆构造查询名。
2. 从宽到窄：先查集群或节点维度确认是否存在整体压力，再用 top 参数查 Namespace 或 Pod 维度定位来源。
3. 回看窗口要覆盖问题发生的时间。默认窗口只有一小时，排查昨天的事故必须显式放大 minutes。
4. 用量结论必须和申请量/限制量一起看：使用率高但远低于 limit 是正常工作，使用率贴着 limit 才是瓶颈。
   list_nodes 与 cluster_overview 提供容量与分配的另一半事实。
5. 指标查询可能返回 partial 或 issues，表示部分集群或部分时间段没有数据。此时不能把结果当作完整事实，
   要在结论里写明覆盖范围。

## 结论要求
给出具体数值、时间窗口和对象，例如「过去 6 小时 ns/web 的 CPU 使用率在 P95 达到 limit 的 96%」。
没有指标数据时直接说明这个部署没有安装多集群指标或该窗口没有样本，不要用一次 describe 的快照冒充趋势。`,
		},
		{
			ID:      "controlled-change",
			Title:   "受控变更与回滚",
			Summary: "需要改变集群时的固定顺序：先取证、再预检、再按预检结果提交，并说明影响与回退方式。",
			Tools: []string{
				"describe_resource", "get_resource", "list_workload_revisions",
				"preview_workload_scale", "preview_manifest_apply", "preview_workload_rollback",
			},
			Body: `# 受控变更与回滚

## 何时使用
结论已经明确，需要真正改变集群：伸缩、回滚、应用或删除 Manifest。

## 固定顺序
1. 先确认当前状态。改之前读一次目标对象，写清它现在是什么样：副本数、镜像、当前 revision。
   没有当前状态就没有回退依据。
2. 再预检。伸缩用 preview_workload_scale；Manifest 用 preview_manifest_apply / preview_manifest_delete；
   回滚用 list_workload_revisions 选定 revision 后 preview_workload_rollback。
   预检是服务端 DryRun，不改变集群，也是你唯一能在提交前看到差异的地方。
3. 把预检结果讲给用户听：会创建/更新/删除哪些对象、哪些字段会变、有没有被判定为敏感。
4. 再提交。Manifest 与回滚只接受预检返回的 preview_id，不要自己构造或修改它，也不要在提交时换一份 YAML。
5. 提交后验证。重新读目标对象与它的 Event，确认变更真的生效；不要把「提交成功」直接说成「问题已解决」。

## 边界
- 一次只做一件事。把伸缩、镜像更新和删除混在一步里，出问题时无法判断是哪一步造成的。
- 不要提交 Secret 清单。AIOps 会拒绝，Secret 只能从 ZKE 的 Secret 专用入口修改。
- 被用户拒绝的调用不是失败：说明为什么建议它，然后在不执行的前提下继续或停下。
- 结论里要写明回退方式：伸缩回原副本数、回滚到原 revision，或删除刚创建的对象。`,
		},
	}
}
