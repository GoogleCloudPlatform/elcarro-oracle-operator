package monitoring

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/cgroups/fs"
	"github.com/opencontainers/runc/libcontainer/cgroups/fs2"
	"github.com/prometheus/client_golang/prometheus"
)

// How a column value should be used.
// It will either be a label value applied to the entire MetricSet
// or a metric value.
type Usage string

const (
	Label           Usage = "label"
	Gauge                 = "gauge"
	Counter               = "counter"
	Histogram             = "histogram"
	volumeNameLabel       = "volume_name"
)

const WORKER_COUNT = 3

var (
	neededSystem = map[string]bool{
		"cpu":     true,
		"cpuacct": true,
		"memory":  true,
	}

	// for test
	getDuration = func(start time.Time) int64 {
		return time.Now().Sub(start).Milliseconds()

	}
)

// diskStats represents used and total disk space in bytes.
type diskStats struct {
	Used  int64
	Total int64
}

// A set of metrics that will be reported to prometheus
// Metrics/labels are derived from the columns of the query.
// The set of metrics reported will be Namespace_Name_Metric.Name for every non-label
// Metric in the MetricSet.
//
// You must use ReadConfig or StartMonitoring to fill out this struct correctly.
type MetricSet struct {
	Name      string   `yaml:"name"`
	Namespace string   `yaml:"namespace"`
	Query     string   `yaml:"query"`
	Metrics   []Metric `yaml:"metrics"`
}

// Specifies a metric within the MetricSet, its Name (which is also the column
// name that will provide its value), the Usage determines what kind of Metric
// this portion of the query represents
//
// When specifying a histogram metric the defined column name will be used as
// the base name of buckets+2 columns that must be in the MetricSet's query.
// `Column_key` for each bucket key and `Column_count`,`Column_sum` for the
// total event count and total sum of events.
type Metric struct {
	Name  string `yaml:"name"`
	Desc  string `yaml:"desc"`
	Usage Usage  `yaml:"usage"`

	// internal
	column string
	// Only for Histograms, defines buckets of the histogram
	Buckets map[string]float64 `yaml:"buckets,omitempty"`
}

// Allow users to pass in the driver specific db connector without this code
// needing a direct dependency to potential C code.
type DBFactory interface {
	Open() (*sql.DB, error)
}

type Monitor struct {
	DBFactory   DBFactory
	MetricSets  []MetricSet
	collectMS   prometheus.Gauge
	metricCount prometheus.Gauge
	errCount    prometheus.Gauge
	log         logr.Logger
}

func validPromName(n string) error {
	// https://prometheus.io/docs/concepts/data_model/#metric-names-and-labels
	validChars := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_:"
	invalid := strings.Trim(n, validChars)
	if len(invalid) != 0 {
		return fmt.Errorf("invalid prometheus characters in %s: %q", n, invalid)
	}
	return nil
}

// Numbers pass through as oracle exponential form we need to avoid this.
func parseString(log logr.Logger, i interface{}) string {
	switch v := i.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int, uint, int32, uint32, int64, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%f", v)
	case time.Time:
		return fmt.Sprintf("%d", v.Unix())
	case nil: // Null values
		return ""
	}
	log.Info("Failed to parse string", "type", fmt.Sprintf("%T", i), "input", i)
	return ""
}
func parseUint64(log logr.Logger, i interface{}) uint64 {
	switch v := i.(type) {
	case string:
		val, _ := strconv.ParseUint(v, 10, 64)
		return val
	case []byte:
		val, _ := strconv.ParseUint(string(v), 10, 64)
		return val
	case int, uint, int32, uint32, uint64:
		return v.(uint64)
	case int64:
		return uint64(v)
	case float32, float64:
		return uint64(v.(float64))
	case time.Time:
		return uint64(v.Unix())
	case nil: // Null values
		return 0
	}
	log.Info("Failed to parse uint", "type", fmt.Sprintf("%T", i), "input", i)
	return 0
}
func parseFloat64(log logr.Logger, i interface{}) float64 {
	switch v := i.(type) {
	case string:
		val, _ := strconv.ParseFloat(v, 10)
		return val
	case []byte:
		val, _ := strconv.ParseFloat(string(v), 10)
		return val
	case int, uint, int32, uint32, int64, uint64:
		return float64(v.(int64))
	case float32, float64:
		return v.(float64)
	case time.Time:
		return float64(v.Unix())
	case nil: // Null values
		return 0
	}
	log.Info("Failed to parse float", "type", fmt.Sprintf("%T", i), "input", i)
	return 0
}

func sortedKeys(m map[string]float64) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Given the database connection and the metricset, do the queries and make the
// metric. All metrics are float64 types, all labels must be strings.  errCount
// should be atomically incremented for each error occured and will be reported
// by prometheus.
func queryMetrics(log logr.Logger, db *sql.DB, ms MetricSet, errCount *uint64) []prometheus.Metric {
	rows, err := db.Query(ms.Query)
	if err != nil {
		atomic.AddUint64(errCount, 1)
		log.Error(err, "Failed to query metric set", "query", ms.Query)
		return nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		atomic.AddUint64(errCount, 1)
		log.Error(err, "Failed to read query columns", "query", ms.Query)
		return nil
	}

	// Force columns to match config (lower case).
	for i := 0; i < len(columns); i++ {
		columns[i] = strings.ToLower(columns[i])
	}

	labels := map[string]string{}
	var metrics []prometheus.Metric
	values := make([]interface{}, len(columns))

	// Setup pointers to interfaces for splatting into rows.Scan
	valuePtrs := make([]interface{}, len(columns))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	// Find the columns for our metrics based on Name.
	// For histograms provide the bucket, sum, count column indexes in that
	// order.
	forMetric := make([]int, len(ms.Metrics))
	forHistMetric := make([][]int, len(ms.Metrics))

	columnToIdx := map[string]int{}
	for i, c := range columns {
		columnToIdx[c] = i
	}

	// Build mapping arrays.
	for i, m := range ms.Metrics {
		if m.Usage == Histogram {
			// [b1,b1,...,bn,sum,count]
			forHistMetric[i] = make([]int, len(m.Buckets)+2)
			j := 0
			for _, k := range sortedKeys(m.Buckets) {
				if idx, found := columnToIdx[fmt.Sprintf("%s_%s", m.column, k)]; found {
					forHistMetric[i][j] = idx
				}
				j += 1
			}
			if idx, found := columnToIdx[m.column+"_sum"]; found {
				forHistMetric[i][j] = idx
			}
			if idx, found := columnToIdx[m.column+"_count"]; found {
				forHistMetric[i][j+1] = idx
			}
		} else if idx, found := columnToIdx[m.column]; found {
			forMetric[i] = idx
		}
	}

	rowCount := 0
	for rows.Next() {
		rows.Scan(valuePtrs...)
		rowCount += 1

		// Build labels from the query first
		for i := range ms.Metrics {
			if ms.Metrics[i].Usage == Label {
				labels[ms.Metrics[i].Name] = parseString(log, values[forMetric[i]])
			}
		}

		// Build metrics
		for i := range ms.Metrics {
			m := ms.Metrics[i]

			mDesc := prometheus.NewDesc(fmt.Sprintf("%s_%s_%s", ms.Namespace, ms.Name, m.Name), m.Desc, nil, labels)
			var metric prometheus.Metric
			var err error
			switch m.Usage {
			case Counter:
				metric, err = prometheus.NewConstMetric(mDesc, prometheus.CounterValue, parseFloat64(log, values[forMetric[i]]))
			case Gauge:
				metric, err = prometheus.NewConstMetric(mDesc, prometheus.GaugeValue, parseFloat64(log, values[forMetric[i]]))
			case Histogram:
				bucketVals := map[float64]uint64{}
				j := 0
				for _, k := range sortedKeys(m.Buckets) {
					bucketVals[m.Buckets[k]] = parseUint64(log, values[forHistMetric[i][j]])
					j += 1
				}
				sum := parseFloat64(log, values[forHistMetric[i][j]])
				count := parseUint64(log, values[forHistMetric[i][j+1]])
				metric, err = prometheus.NewConstHistogram(mDesc, count, sum, bucketVals)
			case Label:
				continue
			}

			if err != nil || metric == nil {
				atomic.AddUint64(errCount, 1)
				log.Error(err, "Failed to create prometheus metric", "desc", mDesc, "err", err, "metric", metric)
				continue
			}
			metrics = append(metrics, metric)
		}
	}
	if rowCount == 0 {
		// This is likely due to bad row level security or a poor query.
		// If you dont see metrics you expected this might be a cause so lets log it.
		log.Info("Query returned no rows.", "query", ms.Query)
	}

	return metrics
}

// NewMonitor prepares a monitor that can be passed to prometheus as a
// Collector. Alternately you can use StartExporting to handle creation of the
// monitor and setting up promtheus.
func NewMonitor(log logr.Logger, db DBFactory, ms []MetricSet) *Monitor {
	mon := &Monitor{
		DBFactory:  db,
		MetricSets: ms,
		collectMS: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "db_monitor",
			Name:      "collect_ms",
			Help:      "Number of milliseconds spent to collect metrics",
		}),
		metricCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "db_monitor",
			Name:      "metric_count",
			Help:      "Number of metrics collected successfully from config this cycle",
		}),
		errCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "db_monitor",
			Name:      "error_count",
			Help:      "Number of errors encountered while trying to collect metrics this cycle",
		}),
		log: log,
	}

	return mon
}

// Describe intentionally left blank as we will dynamically be generating metrics.
func (m *Monitor) Describe(ch chan<- *prometheus.Desc) {}

// Collect for prometheus.Collector interface, called when we should report metrics.
// TODO thread contexts.
func (m *Monitor) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	errCount := uint64(0)
	metricCount := uint64(0)
	msQueue := make(chan MetricSet)

	started := 0
	var wg sync.WaitGroup
	for i := 0; i < WORKER_COUNT; i++ {
		// Ensure workers can do work before starting them.
		db, err := m.DBFactory.Open()
		if err != nil {
			atomic.AddUint64(&errCount, 1)
			continue
		}

		if err := db.Ping(); err != nil {
			atomic.AddUint64(&errCount, 1)
			m.log.Error(err, "failed to connect to database", "collector", i)
			continue
		}
		wg.Add(1)
		started += 1
		go func(i int, db *sql.DB) {
			defer wg.Done()
			for {
				ms, more := <-msQueue
				if !more { // all metrics collected.
					m.log.V(2).Info("done", "collector", i)
					return
				}
				for _, metric := range queryMetrics(m.log, db, ms, &errCount) {
					ch <- metric
					atomic.AddUint64(&metricCount, 1)
				}
			}
		}(i, db)
	}

	// Cant queue work if we didnt start any connections successfully
	if started > 0 {
		m.log.V(2).Info("queueing work")
		for _, ms := range m.MetricSets {
			msQueue <- ms
		}
	}

	// Wait for metrics to be collected and reported.
	close(msQueue)
	wg.Wait()

	duration := getDuration(start)
	m.collectMS.Set(float64(duration))
	m.metricCount.Set(float64(metricCount))
	m.errCount.Set(float64(errCount))

	ch <- m.collectMS
	ch <- m.metricCount
	ch <- m.errCount

	m.log.Info("reported metrics", "count", metricCount, "errors", errCount, "time(ms)", duration)
}

// VolumeMetrics specifies a pod volume metrics
type VolumeMetrics struct {
	// usage of volume in bytes
	Usage prometheus.Gauge
	// total available space of volume in bytes
	Total prometheus.Gauge
}

// VolumeInfo provides required information to expose pod volume metrics
type VolumeInfo struct {
	Mount string
	Name  string
}

// DBContainerMonitor metrics followed http://google3/configs/monitoring/cloud_pulse_monarch/kubernetes/metrics_def_core
// this is a workaround if a platform does not provide system metrics(container CPU/memory, volumes) to DB users.
type DBContainerMonitor struct {
	// usage of memory in bytes
	MemoryUsage prometheus.Gauge
	// memory used for cache
	MemoryCacheUsage prometheus.Gauge

	mountToMetrics map[string]VolumeMetrics
	collectMS      prometheus.Gauge
	metricCount    prometheus.Gauge
	errCount       prometheus.Gauge
	log            logr.Logger
}

// NewDBContainerMonitor prepares a monitor that can be passed to prometheus as a
// Collector to collect container system(memory/CPU) metrics
func NewDBContainerMonitor(log logr.Logger, volumes []VolumeInfo) *DBContainerMonitor {
	mon := &DBContainerMonitor{
		MemoryUsage: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "ods_container",
			Name:      "memory_used_bytes",
			Help:      "Memory usage in bytes.",
		}),
		MemoryCacheUsage: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "ods_container",
			Name:      "memory_cache_used_bytes",
			Help:      "Cache memory usage in bytes.",
		}),
		collectMS: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "ods_system_monitor",
			Name:      "collect_ms",
			Help:      "Number of milliseconds spent to collect metrics",
		}),
		metricCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "ods_system_monitor",
			Name:      "metric_count",
			Help:      "Number of metrics collected successfully from config this cycle",
		}),
		errCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "ods_system_monitor",
			Name:      "error_count",
			Help:      "Number of errors encountered while trying to collect metrics this cycle",
		}),
		log: log,
	}

	if len(volumes) > 0 {
		mon.mountToMetrics = make(map[string]VolumeMetrics)
	}
	for _, v := range volumes {
		log.Info("Adding volume metrics", "volume", v.Name, "mount", v.Mount)
		mon.mountToMetrics[v.Mount] = VolumeMetrics{
			Usage: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace:   "ods_pod_volume",
				Name:        "used_bytes",
				Help:        "Number of disk bytes used by the pod volume.",
				ConstLabels: map[string]string{volumeNameLabel: v.Name},
			}),
			Total: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace:   "ods_pod_volume",
				Name:        "total_bytes",
				Help:        "Total number of disk bytes available to the pod volume.",
				ConstLabels: map[string]string{volumeNameLabel: v.Name},
			}),
		}
	}

	return mon
}

func (m *DBContainerMonitor) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	errCount := uint64(0)
	metricCount := uint64(0)

	if cstats, err := cgroupStats(m.log); err == nil {
		m.MemoryUsage.Set(float64(cstats.MemoryStats.Usage.Usage))
		m.MemoryCacheUsage.Set(float64(cstats.MemoryStats.Cache))
		for _, m := range []prometheus.Metric{m.MemoryUsage, m.MemoryCacheUsage} {
			ch <- m
			metricCount++
		}
		// Total CPU time consumed in seconds. `
		// unit follow http://google3/configs/monitoring/cloud_pulse_monarch/kubernetes/metrics_def_core;l=51;rcl=448039408
		if CPUTotalUsage, err := prometheus.NewConstMetric(
			prometheus.NewDesc("ods_container_cpu_usage_time_seconds", "Cumulative CPU usage on all cores used by the container in seconds.", nil, nil),
			prometheus.CounterValue,
			float64(cstats.CpuStats.CpuUsage.TotalUsage/1000_000_000), // in seconds
		); err == nil {
			ch <- CPUTotalUsage
			metricCount++
		} else {
			m.log.Error(err, "error while reporting CPU metrics")
			errCount++
		}
	} else {
		m.log.Error(err, "error while parsing cgroup for metrics")
		errCount++
	}

	for mount, metrics := range m.mountToMetrics {
		if stats, err := dStats(mount); err == nil {
			metrics.Total.Set(float64(stats.Total))
			metrics.Usage.Set(float64(stats.Used))
			ch <- metrics.Total
			ch <- metrics.Usage
			metricCount += 2
		} else {
			m.log.Error(err, "error while reading disk stats", "mount", mount)
			errCount++
		}
	}

	duration := getDuration(start)
	m.collectMS.Set(float64(duration))
	m.metricCount.Set(float64(metricCount))
	m.errCount.Set(float64(errCount))
	ch <- m.collectMS
	ch <- m.metricCount
	ch <- m.errCount

	m.log.Info("reported metrics", "count", metricCount, "errors", errCount, "time(ms)", duration)
}

// Describe intentionally left blank as we will dynamically be generating metrics.
func (m *DBContainerMonitor) Describe(ch chan<- *prometheus.Desc) {}

// followed https://github.com/google/cadvisor/blob/3beb265804ea4b00dc8ed9125f1f71d3328a7a94/container/libcontainer/helpers.go#L95
var cgroupStats = func(log logr.Logger) (*cgroups.Stats, error) {
	if cgroups.IsCgroup2UnifiedMode() {
		m, err := fs2.NewManager(nil, fs2.UnifiedMountpoint)
		if err != nil {
			return nil, err
		}
		return m.GetStats()
	}

	mounts, err := cgroups.GetCgroupMounts(true)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]string, len(mounts))
	for _, m := range mounts {
		for _, subsystem := range m.Subsystems {
			if !neededSystem[subsystem] {
				continue
			}
			if existing, ok := paths[subsystem]; ok {
				log.Info("skipping current mount point for a sub system", "existing mount", existing, "current mount", m.Mountpoint)
				continue
			}
			paths[subsystem] = m.Mountpoint
		}
	}
	mgr, err := fs.NewManager(nil, paths)
	if err != nil {
		return nil, err
	}
	return mgr.GetStats()
}

// dStats returns the diskStats for the provided mount point, or an error.
var dStats = func(path string) (diskStats, error) {
	f := syscall.Statfs_t{}
	err := syscall.Statfs(path, &f)
	if err != nil {
		return diskStats{}, err
	}

	var stats diskStats
	stats.Total = int64(f.Blocks) * f.Bsize
	free := int64(f.Bfree) * f.Bsize
	stats.Used = stats.Total - free
	return stats, nil
}
