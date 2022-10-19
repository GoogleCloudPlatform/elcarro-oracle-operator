package monitoring

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-logr/logr"
	"github.com/golang/protobuf/proto"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/prometheus/client_golang/prometheus"
	prometheuspb "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/testing/protocmp"
	"k8s.io/klog/v2"
)

type mockFact struct {
	setup func(sqlmock.Sqlmock)
	db    *sql.DB
	mock  sqlmock.Sqlmock
}

func (d *mockFact) Open() (*sql.DB, error) {
	db, mock, _ := sqlmock.New()
	d.setup(mock)
	return db, nil
}

func mustUnmarshal(s string) *prometheuspb.Metric {
	m := prometheuspb.Metric{}
	if err := proto.UnmarshalText(s, &m); err != nil {
		panic(fmt.Sprintf("Failed to unmarshal protobuf: %s", s))
	}
	return &m
}

var metricCompare = cmp.Comparer(metricCompareFunc)

func metricCompareFunc(a, b prometheus.Metric) bool {
	var am, bm prometheuspb.Metric
	a.Write(&am)
	b.Write(&bm)

	return cmp.Equal(am, bm, protocmp.Transform())
}

type Result struct {
	Desc   string
	Metric *prometheuspb.Metric
}

func containsAny(s string, set []string) bool {
	for _, e := range set {
		if strings.Contains(s, e) {
			return true
		}
	}
	return false
}

func ignoreMetaMetrics(r Result) bool {
	metaDescs := []string{
		"db_monitor_collect_ms",
		"db_monitor_metric_count",
		"db_monitor_error_count",
	}
	return containsAny(r.Desc, metaDescs)
}

func ignoreNonMetaMetrics(r Result) bool {
	// Always ignore this one as it measures time and can flake.
	if strings.Contains(r.Desc, "db_monitor_collect_ms") {
		return true
	}
	metaDescs := []string{
		"db_monitor_metric_count",
		"db_monitor_error_count",
	}
	return !containsAny(r.Desc, metaDescs)
}

func TestCollect(t *testing.T) {
	tests := []struct {
		name   string
		db     DBFactory
		ms     []MetricSet
		want   []Result
		ignore func(r Result) bool
	}{{
		name: "Collect 1 metric",
		db: &mockFact{setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("query1").
				WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow(22))
		}},
		ms: []MetricSet{{
			Namespace: "ns",
			Name:      "n",
			Query:     "query1",
			Metrics: []Metric{{
				Name:   "m",
				Desc:   "d",
				column: "m",
				Usage:  Gauge,
			}},
		}},
		want: []Result{{
			`Desc{fqName: "ns_n_m", help: "d", constLabels: {}, variableLabels: []}`,
			mustUnmarshal(`gauge: {
				value: 22.0
			}`),
		}},
		ignore: ignoreMetaMetrics,
	}, {
		name: "Collect 3 metric with label",
		db: &mockFact{setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("query1").WillReturnRows(
				sqlmock.NewRows([]string{"m", "m2", "hist_count", "hist_sum", "hist_b1", "hist_b2", "l"}).
					AddRow(22, 33, 10, 100, 50, 51, "label1"))
		}},
		ms: []MetricSet{{
			Namespace: "ns",
			Name:      "n",
			Query:     "query1",
			Metrics: []Metric{{
				Name:   "m",
				Desc:   "d",
				column: "m",
				Usage:  Gauge,
			}, {
				Name:   "m2",
				column: "m2",
				Usage:  Counter,
			}, {
				Name:    "hist",
				column:  "hist",
				Usage:   Histogram,
				Buckets: map[string]float64{"b1": 50.0, "b2": 100},
			}, {
				Name:   "l",
				column: "l",
				Usage:  Label,
			}},
		}},
		want: []Result{{
			`Desc{fqName: "ns_n_m", help: "d", constLabels: {l="label1"}, variableLabels: []}`,
			mustUnmarshal(`
			label: { name: "l", value: "label1" }
			gauge: { value: 22.0 }
			`),
		}, {
			`Desc{fqName: "ns_n_m2", help: "", constLabels: {l="label1"}, variableLabels: []}`,
			mustUnmarshal(`
			label: { name: "l", value: "label1" }
			counter: { value: 33.0 }
			`),
		}, {
			`Desc{fqName: "ns_n_hist", help: "", constLabels: {l="label1"}, variableLabels: []}`,
			mustUnmarshal(`
			label: { name: "l", value: "label1" }
			histogram: {
				sample_count: 10
				sample_sum: 100
				bucket: {
					cumulative_count: 50
					upper_bound: 50.0
				}
				bucket: {
					cumulative_count: 51
					upper_bound: 100.0
				}
			}
			`),
		}},
		ignore: ignoreMetaMetrics,
	}, {
		name: "Collect meta metrics",
		db: &mockFact{setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("query1").
				WillReturnRows(sqlmock.NewRows([]string{"m", "m2", "l"}).AddRow(22, 33, "label1"))
		}},
		ms: []MetricSet{{
			Namespace: "ns",
			Name:      "n",
			Query:     "query1",
			Metrics: []Metric{{
				Name:   "m",
				column: "m",
				Usage:  Gauge,
			}, {
				Name:   "m2",
				column: "m2",
				Usage:  Gauge,
			}, {
				Name:   "l",
				column: "l",
				Usage:  Label,
			}},
		}},
		ignore: ignoreNonMetaMetrics,
		want: []Result{{
			`Desc{fqName: "db_monitor_metric_count", help: "Number of metrics collected successfully from config this cycle", constLabels: {}, variableLabels: []}`,
			mustUnmarshal(`gauge: {
					value: 2.0
				}`),
		}, {
			`Desc{fqName: "db_monitor_error_count", help: "Number of errors encountered while trying to collect metrics this cycle", constLabels: {}, variableLabels: []}`,
			mustUnmarshal(`gauge: {
					value: 0.0
				}`),
		}},
	}}

	for _, test := range tests {
		mon := NewMonitor(klog.NewKlogr(), test.db, test.ms)
		ch := make(chan prometheus.Metric)
		go func() {
			mon.Collect(ch)
			close(ch)
		}()

		got := []Result{}
		for m := range ch {
			if m == nil {
				t.Errorf("Nil metric reported")
				continue
			}

			var mpb prometheuspb.Metric
			m.Write(&mpb)
			got = append(got, Result{m.Desc().String(), &mpb})
		}
		opts := []cmp.Option{
			protocmp.Transform(),
		}
		if test.ignore != nil {
			opts = append(opts, cmpopts.IgnoreSliceElements(test.ignore))
		}

		if diff := cmp.Diff(test.want, got, opts...); diff != "" {
			t.Errorf("Metrics diff (-want +got):\n%s", diff)
		}
	}
}

func TestNewDBContainerMonitor(t *testing.T) {
	base := func() *DBContainerMonitor {
		return &DBContainerMonitor{
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
				Namespace: "ods_pod_volume_monitor",
				Name:      "error_count",
				Help:      "Number of errors encountered while trying to collect metrics this cycle",
			}),
		}
	}

	type args struct {
		log     logr.Logger
		volumes []VolumeInfo
	}
	tests := []struct {
		name string
		args args
		want func() *DBContainerMonitor
	}{
		{
			name: "no volume",
			args: args{
				log:     klog.NewKlogr(),
				volumes: []VolumeInfo{},
			},
			want: base,
		},
		{
			name: "one volume",
			args: args{
				log: klog.NewKlogr(),
				volumes: []VolumeInfo{
					{
						Mount: "/pgdata",
						Name:  "DataDisk",
					},
				},
			},
			want: func() *DBContainerMonitor {
				w := base()
				w.mountToMetrics = map[string]VolumeMetrics{
					"/pgdata": {
						Usage: prometheus.NewGauge(prometheus.GaugeOpts{
							Namespace:   "ods_pod_volume",
							Name:        "used_bytes",
							Help:        "Number of disk bytes used by the pod volume.",
							ConstLabels: map[string]string{volumeNameLabel: "DataDisk"},
						}),
						Total: prometheus.NewGauge(prometheus.GaugeOpts{
							Namespace:   "ods_pod_volume",
							Name:        "total_bytes",
							Help:        "Total number of disk bytes available to the pod volume.",
							ConstLabels: map[string]string{volumeNameLabel: "DataDisk"},
						}),
					},
				}
				return w
			},
		},
		{
			name: "two volumes",
			args: args{
				log: klog.NewKlogr(),
				volumes: []VolumeInfo{
					{
						Mount: "/pgdata",
						Name:  "DataDisk",
					},
					{
						Mount: "/obs",
						Name:  "ObsDisk",
					},
				},
			},
			want: func() *DBContainerMonitor {
				w := base()
				w.mountToMetrics = map[string]VolumeMetrics{
					"/pgdata": {
						Usage: prometheus.NewGauge(prometheus.GaugeOpts{
							Namespace:   "ods_pod_volume",
							Name:        "used_bytes",
							Help:        "Number of disk bytes used by the pod volume.",
							ConstLabels: map[string]string{volumeNameLabel: "DataDisk"},
						}),
						Total: prometheus.NewGauge(prometheus.GaugeOpts{
							Namespace:   "ods_pod_volume",
							Name:        "total_bytes",
							Help:        "Total number of disk bytes available to the pod volume.",
							ConstLabels: map[string]string{volumeNameLabel: "DataDisk"},
						}),
					},
					"/obs": {
						Usage: prometheus.NewGauge(prometheus.GaugeOpts{
							Namespace:   "ods_pod_volume",
							Name:        "used_bytes",
							Help:        "Number of disk bytes used by the pod volume.",
							ConstLabels: map[string]string{volumeNameLabel: "ObsDisk"},
						}),
						Total: prometheus.NewGauge(prometheus.GaugeOpts{
							Namespace:   "ods_pod_volume",
							Name:        "total_bytes",
							Help:        "Total number of disk bytes available to the pod volume.",
							ConstLabels: map[string]string{volumeNameLabel: "ObsDisk"},
						}),
					},
				}
				return w
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewDBContainerMonitor(tt.args.log, tt.args.volumes)
			want := tt.want()
			if !cmp.Equal(got.mountToMetrics, want.mountToMetrics, metricCompare) {
				t.Errorf("NewDBContainerMonitor() mountToMetrics got %v, want %v", got, want)
			}
		})
	}
}

func TestContainerMonitor_Collect(t *testing.T) {
	type args struct {
		ch chan prometheus.Metric
	}
	tests := []struct {
		name         string
		args         args
		volumes      []VolumeInfo
		mockC        func(log logr.Logger) (*cgroups.Stats, error)
		mockD        func(path string) (diskStats, error)
		mockDuration func(start time.Time) int64
		want         []Result
	}{
		{
			name: "get CPU memory and volume",
			args: args{
				ch: make(chan prometheus.Metric),
			},
			volumes: []VolumeInfo{
				{
					Mount: "/pgdata",
					Name:  "DataDisk",
				},
				{
					Mount: "/obs",
					Name:  "ObsDisk",
				},
			},
			mockC: func(log logr.Logger) (*cgroups.Stats, error) {
				return &cgroups.Stats{
					CpuStats: cgroups.CpuStats{
						CpuUsage: cgroups.CpuUsage{
							TotalUsage: 2000000000, // in ns
						},
					},
					MemoryStats: cgroups.MemoryStats{
						Cache: 55000000,
						Usage: cgroups.MemoryData{
							Usage: 100000000,
						},
					},
				}, nil
			},
			mockD: func(path string) (diskStats, error) {
				if path == "/pgdata" {
					return diskStats{
						Used:  4000000000,
						Total: 8000000000,
					}, nil
				}
				if path == "/obs" {
					return diskStats{
						Used:  1000000000,
						Total: 2000000000,
					}, nil
				}
				return diskStats{}, fmt.Errorf("mock stats not found for %s", path)
			},
			mockDuration: func(_ time.Time) int64 {
				return 30
			},
			want: []Result{
				{
					`Desc{fqName: "ods_container_memory_used_bytes", help: "Memory usage in bytes.", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 100000000
				}`),
				},
				{
					`Desc{fqName: "ods_container_memory_cache_used_bytes", help: "Cache memory usage in bytes.", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 55000000
				}`),
				},
				{
					`Desc{fqName: "ods_container_cpu_usage_time_seconds", help: "Cumulative CPU usage on all cores used by the container in seconds.", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`counter: {
					value: 2.0
				}`),
				},
				{
					`Desc{fqName: "ods_pod_volume_total_bytes", help: "Total number of disk bytes available to the pod volume.", constLabels: {volume_name="DataDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 8000000000
				}
label:[{name: "volume_name" value:"DataDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_used_bytes", help: "Number of disk bytes used by the pod volume.", constLabels: {volume_name="DataDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 4000000000
				}
label:[{name: "volume_name" value:"DataDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_total_bytes", help: "Total number of disk bytes available to the pod volume.", constLabels: {volume_name="ObsDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 2000000000
				}
label:[{name: "volume_name" value:"ObsDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_used_bytes", help: "Number of disk bytes used by the pod volume.", constLabels: {volume_name="ObsDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 1000000000
				}
label:[{name: "volume_name" value:"ObsDisk"}]`),
				},
				{
					`Desc{fqName: "ods_system_monitor_collect_ms", help: "Number of milliseconds spent to collect metrics", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 30
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_metric_count", help: "Number of metrics collected successfully from config this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 7.0
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_error_count", help: "Number of errors encountered while trying to collect metrics this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 0.0
				}`),
				},
			},
		},
		{
			name: "volume failed one, get CPU memory",
			args: args{
				ch: make(chan prometheus.Metric),
			},
			volumes: []VolumeInfo{
				{
					Mount: "/pgdata",
					Name:  "DataDisk",
				},
				{
					Mount: "/obs",
					Name:  "ObsDisk",
				},
			},
			mockC: func(log logr.Logger) (*cgroups.Stats, error) {
				return &cgroups.Stats{
					CpuStats: cgroups.CpuStats{
						CpuUsage: cgroups.CpuUsage{
							TotalUsage: 2000000000, // in ns
						},
					},
					MemoryStats: cgroups.MemoryStats{
						Cache: 55000000,
						Usage: cgroups.MemoryData{
							Usage: 100000000,
						},
					},
				}, nil
			},
			mockD: func(path string) (diskStats, error) {
				if path == "/obs" {
					return diskStats{
						Used:  1000000000,
						Total: 2000000000,
					}, nil
				}
				return diskStats{}, fmt.Errorf("mock stats not found for %s", path)
			},
			mockDuration: func(_ time.Time) int64 {
				return 30
			},
			want: []Result{
				{
					`Desc{fqName: "ods_container_memory_used_bytes", help: "Memory usage in bytes.", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 100000000
				}`),
				},
				{
					`Desc{fqName: "ods_container_memory_cache_used_bytes", help: "Cache memory usage in bytes.", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 55000000
				}`),
				},
				{
					`Desc{fqName: "ods_container_cpu_usage_time_seconds", help: "Cumulative CPU usage on all cores used by the container in seconds.", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`counter: {
					value: 2.0
				}`),
				},
				{
					`Desc{fqName: "ods_pod_volume_total_bytes", help: "Total number of disk bytes available to the pod volume.", constLabels: {volume_name="ObsDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 2000000000
				}
label:[{name: "volume_name" value:"ObsDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_used_bytes", help: "Number of disk bytes used by the pod volume.", constLabels: {volume_name="ObsDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 1000000000
				}
label:[{name: "volume_name" value:"ObsDisk"}]`),
				},
				{
					`Desc{fqName: "ods_system_monitor_collect_ms", help: "Number of milliseconds spent to collect metrics", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 30
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_metric_count", help: "Number of metrics collected successfully from config this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 5.0
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_error_count", help: "Number of errors encountered while trying to collect metrics this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 1.0
				}`),
				},
			},
		},
		{
			name: "CPU and memory failed, get volumes",
			args: args{
				ch: make(chan prometheus.Metric),
			},
			volumes: []VolumeInfo{
				{
					Mount: "/pgdata",
					Name:  "DataDisk",
				},
				{
					Mount: "/obs",
					Name:  "ObsDisk",
				},
			},
			mockC: func(log logr.Logger) (*cgroups.Stats, error) {
				return nil, errors.New("fake")
			},
			mockD: func(path string) (diskStats, error) {
				if path == "/pgdata" {
					return diskStats{
						Used:  4000000000,
						Total: 8000000000,
					}, nil
				}
				if path == "/obs" {
					return diskStats{
						Used:  1000000000,
						Total: 2000000000,
					}, nil
				}
				return diskStats{}, fmt.Errorf("mock stats not found for %s", path)
			},
			mockDuration: func(_ time.Time) int64 {
				return 30
			},
			want: []Result{
				{
					`Desc{fqName: "ods_pod_volume_total_bytes", help: "Total number of disk bytes available to the pod volume.", constLabels: {volume_name="DataDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 8000000000
				}
label:[{name: "volume_name" value:"DataDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_used_bytes", help: "Number of disk bytes used by the pod volume.", constLabels: {volume_name="DataDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 4000000000
				}
label:[{name: "volume_name" value:"DataDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_total_bytes", help: "Total number of disk bytes available to the pod volume.", constLabels: {volume_name="ObsDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 2000000000
				}
label:[{name: "volume_name" value:"ObsDisk"}]`),
				},
				{
					`Desc{fqName: "ods_pod_volume_used_bytes", help: "Number of disk bytes used by the pod volume.", constLabels: {volume_name="ObsDisk"}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 1000000000
				}
label:[{name: "volume_name" value:"ObsDisk"}]`),
				},
				{
					`Desc{fqName: "ods_system_monitor_collect_ms", help: "Number of milliseconds spent to collect metrics", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 30
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_metric_count", help: "Number of metrics collected successfully from config this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 4.0
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_error_count", help: "Number of errors encountered while trying to collect metrics this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 1.0
				}`),
				},
			},
		},
		{
			name: "all failed",
			args: args{
				ch: make(chan prometheus.Metric),
			},
			volumes: []VolumeInfo{
				{
					Mount: "/pgdata",
					Name:  "DataDisk",
				},
				{
					Mount: "/obs",
					Name:  "ObsDisk",
				},
			},
			mockC: func(log logr.Logger) (*cgroups.Stats, error) {
				return nil, errors.New("fake")
			},
			mockD: func(path string) (diskStats, error) {
				return diskStats{}, fmt.Errorf("mock stats not found for %s", path)
			},
			mockDuration: func(_ time.Time) int64 {
				return 30
			},
			want: []Result{
				{
					`Desc{fqName: "ods_system_monitor_collect_ms", help: "Number of milliseconds spent to collect metrics", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 30
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_metric_count", help: "Number of metrics collected successfully from config this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 0.0
				}`),
				},
				{
					`Desc{fqName: "ods_system_monitor_error_count", help: "Number of errors encountered while trying to collect metrics this cycle", constLabels: {}, variableLabels: []}`,
					mustUnmarshal(`gauge: {
					value: 3.0
				}`),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDBContainerMonitor(klog.NewKlogr(), tt.volumes)
			backupC := cgroupStats
			backupD := dStats
			backupDuration := getDuration
			defer func() {
				cgroupStats = backupC
				dStats = backupD
				getDuration = backupDuration
			}()
			cgroupStats = tt.mockC
			dStats = tt.mockD
			getDuration = tt.mockDuration
			go func() {
				m.Collect(tt.args.ch)
				close(tt.args.ch)
			}()
			var got []Result
			for metric := range tt.args.ch {
				if metric == nil {
					t.Errorf("Nil metric reported")
					continue
				}
				var mpb prometheuspb.Metric
				metric.Write(&mpb)
				got = append(got, Result{metric.Desc().String(), &mpb})
			}
			if diff := cmp.Diff(tt.want, got, protocmp.Transform(), cmpopts.SortSlices(func(a, b Result) bool { return a.Desc < b.Desc })); diff != "" {
				t.Errorf("Metrics diff (-want +got):\n%s", diff)
			}
		})
	}
}
