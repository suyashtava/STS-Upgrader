package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	RecyclerRequestCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recycler_request_count",
		Help: "The number of request send from kube-node-recycler to cloud provider",
	})

	RecyclerRequestError = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recycler_request_errors_total",
		Help: "The number of error response from cloud provider",
	})

	RecyclerRequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "recycler_request_duration_seconds",
		Help: "The time duration for cloud provider responses",
	})

	RecyclerOperationError = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recycler_operation_errors_total",
		Help: "The number of error during kube-node-recycler operation",
	})

	RecyclerAPPodScheduledDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "recycler_ap_pod_scheduled_seconds",
		Help:    "The time for artificial pod to be scheduled",
		Buckets: []float64{0, 5, 15, 30, 60, 120, 240, 360, 480, 600, 720, 840},
	})

	RecyclerOperationDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "recycler_operation_duration_seconds",
		Help:    "The node recycle end to end duration in seconds",
		Buckets: []float64{0, 30, 60, 120, 240, 360, 480, 600, 720, 840},
	})

	RecyclerPodEvictionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "recycler_pod_eviction_duration_seconds",
		Help:    "The pod eviction duration of node in seconds",
		Buckets: []float64{0, 30, 60, 120, 240, 360, 480, 600, 720, 840},
	})

	RecyclerNodeScaleUpTimeOut = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_node_scale_up_timeout",
		Help: "The new node scale up time out during node recycle process",
	})

	RecyclerPodEvictionTimeOut = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "recycler_pod_eviction_timeout",
		Help: "The pod eviction timeout during node recycle process",
	}, []string{"instance_name", "namespace", "pod_name"})

	RecyclerUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_up",
		Help: "The kube-node-recycler is up and running",
	})

	RecyclerDriftIgnoredNodeCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_drift_ignored_node_count",
		Help: "The number of drift nodes in the ignored nodepool list",
	})

	RecyclerNeedRecycleIgnoredNodeCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_need_recycle_ignored_node_count",
		Help: "The number of drift nodes that are in the ignored nodepool list and didn't finish recycling",
	})

	RecyclerDriftNodeCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_drift_node_count",
		Help: "The number of drift nodes that need to be cycled by node-recycler",
	})

	RecyclerNeedRecycleNodeCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_need_recycle_node_count",
		Help: "The number of drift node that need to be cycled by node-recycler but didn't finish recycling",
	})

	RecyclerRecycledNodesCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recycler_recycled_node_count",
		Help: "The number of recycled node",
	})

	RecyclerAvailable = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_available",
		Help: "The kube-node-recycler is available",
	})

	RecyclerPDBDeletionCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recycler_pdb_deletion_count",
		Help: "The number of successful pdb deletion",
	})

	RecyclerLeaseAvailable = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recycler_lease_available",
		Help: "The kube-node-recycler nodepool lease is available",
	})
)

func init() {
	registerMetrics()
}

func registerMetrics() {
	prometheus.MustRegister(RecyclerRequestCount)
	prometheus.MustRegister(RecyclerRequestError)
	prometheus.MustRegister(RecyclerRequestDuration)
	prometheus.MustRegister(RecyclerOperationError)
	prometheus.MustRegister(RecyclerAPPodScheduledDuration)
	prometheus.MustRegister(RecyclerPodEvictionDuration)
	prometheus.MustRegister(RecyclerOperationDuration)
	prometheus.MustRegister(RecyclerNodeScaleUpTimeOut)
	prometheus.MustRegister(RecyclerPodEvictionTimeOut)
	prometheus.MustRegister(RecyclerUp)
	prometheus.MustRegister(RecyclerDriftIgnoredNodeCount)
	prometheus.MustRegister(RecyclerNeedRecycleIgnoredNodeCount)
	prometheus.MustRegister(RecyclerDriftNodeCount)
	prometheus.MustRegister(RecyclerNeedRecycleNodeCount)
	prometheus.MustRegister(RecyclerRecycledNodesCount)
	prometheus.MustRegister(RecyclerAvailable)
	prometheus.MustRegister(RecyclerPDBDeletionCount)
}
