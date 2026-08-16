package metricsquery

import (
	"fmt"
	"strings"
)

// Kind separates a query that returns a curve from one that returns a single
// current value.
type Kind string

const (
	KindRange   Kind = "range"
	KindInstant Kind = "instant"
)

// Unit is the unit the Server converts a query's values into before returning
// them, so no client has to know what the underlying metric counts in.
type Unit string

const (
	UnitMillicores Unit = "millicores"
	UnitBytes      Unit = "bytes"
	UnitCount      Unit = "count"
	UnitRatio      Unit = "ratio"
)

// Definition is one query the Console may ask for.
//
// The catalogue exists instead of accepting PromQL from the browser. Two
// reasons, and the second is the load-bearing one: an expression from outside
// would have to be parsed and rewritten before a scope filter could be
// injected, and any flaw in that rewriting is a cross-tenant data leak; and
// the cost of an arbitrary expression cannot be predicted, so one query could
// exhaust the storage a single-instance Server depends on. Here the filter is
// part of the template, and every template's shape is known before it runs.
type Definition struct {
	Name  string
	Title string
	Kind  Kind
	Unit  Unit
	// Dimensions are the labels each series carries besides the Cluster
	// identity, in the order a client should display them.
	Dimensions        []string
	RequiresNamespace bool
	SupportsTop       bool
	// RequiresTop marks a query whose natural answer is unbounded. Pod level is
	// the first such dimension: a Cluster has orders of magnitude more Pods than
	// Nodes, and "every Pod" is neither renderable nor a question anyone asks.
	// Demanding Top N in the request keeps that bound in the contract instead of
	// leaving it to whatever the series ceiling happens to cut off.
	RequiresTop bool
	// SupportsNamespace marks a query whose expression can carry a Namespace
	// filter. Asking for one elsewhere is refused rather than ignored, so a
	// caller never believes it narrowed an answer that it did not.
	SupportsNamespace bool
	// build receives an already-validated label matcher and parameters. It
	// never sees raw client input.
	build func(matcher string, params buildParams) string
}

type buildParams struct {
	Namespace string
	Top       int
	Window    string
}

// The kubelet resource endpoint is the only scrape target the generated
// collector manifest configures, so every query here is expressed over the
// metrics it exposes. Adding a target means adding queries deliberately, not
// discovering them.
func catalog() []Definition {
	return []Definition{
		{
			Name:  "cluster_cpu_usage",
			Title: "集群 CPU 用量",
			Kind:  KindRange,
			Unit:  UnitMillicores,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) (rate(node_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					matcher,
					params.Window,
				)
			},
		},
		{
			Name:  "cluster_memory_usage",
			Title: "集群内存用量",
			Kind:  KindRange,
			Unit:  UnitBytes,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) (node_memory_working_set_bytes{%s})`,
					matcher,
				)
			},
		},
		{
			Name:        "node_cpu_usage",
			Title:       "节点 CPU 用量",
			Kind:        KindRange,
			Unit:        UnitMillicores,
			Dimensions:  []string{"node"},
			SupportsTop: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, node) (rate(node_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					matcher,
					params.Window,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:        "node_memory_usage",
			Title:       "节点内存用量",
			Kind:        KindRange,
			Unit:        UnitBytes,
			Dimensions:  []string{"node"},
			SupportsTop: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, node) (node_memory_working_set_bytes{%s})`,
					matcher,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:              "namespace_cpu_usage",
			Title:             "命名空间 CPU 用量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) (rate(pod_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:              "namespace_memory_usage",
			Title:             "命名空间内存用量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) (pod_memory_working_set_bytes{%s})`,
					namespaceSelector(matcher, params.Namespace),
				)
				return topk(expression, params.Top)
			},
		},
		{
			// Pod level comes from the same kubelet endpoint the Cluster and Node
			// views already read, so it needs no additional scrape target and adds
			// no cardinality to what a Cluster ships. Only the answer is larger,
			// which is what RequiresTop bounds.
			Name:              "pod_cpu_usage",
			Title:             "Pod CPU 用量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) (rate(pod_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:              "pod_memory_usage",
			Title:             "Pod 内存用量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) (pod_memory_working_set_bytes{%s})`,
					namespaceSelector(matcher, params.Namespace),
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:  "collection_health",
			Title: "采集健康度",
			Kind:  KindInstant,
			Unit:  UnitRatio,
			build: func(matcher string, _ buildParams) string {
				// The fraction of this Cluster's scrape targets that answered
				// their last scrape. A Cluster missing from the answer entirely
				// has sent nothing recently, which the caller must show as
				// "no data" rather than as zero.
				return fmt.Sprintf(
					`avg by (zke_cluster_id) (up{%s})`,
					matcher,
				)
			},
		},
	}
}

// namespaceSelector narrows a matcher to one Namespace. The value is a
// validated DNS label by the time it arrives, so it cannot close the selector
// and open something else.
func namespaceSelector(matcher string, namespace string) string {
	if namespace == "" {
		return matcher
	}
	return fmt.Sprintf(`%s,namespace="%s"`, matcher, namespace)
}

func topk(expression string, top int) string {
	if top <= 0 {
		return expression
	}
	return fmt.Sprintf("topk(%d, %s)", top, expression)
}

// Catalog reports the queries this Server offers.
func Catalog() []Definition {
	return catalog()
}

func lookup(name string) (Definition, bool) {
	for _, definition := range catalog() {
		if definition.Name == strings.TrimSpace(name) {
			return definition, true
		}
	}
	return Definition{}, false
}
