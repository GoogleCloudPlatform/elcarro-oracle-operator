// Copyright 2021 Google LLC
//
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file or at
// https://opensource.org/licenses/MIT.

// Package monitoring is used for monitoring agent.
// This is based off iamseth/oracledb_exporter.
// The significant difference is the reliance on yaml instead of toml
// and the use of gRPC via dbdaemon instead of oracle client and tcp.
package monitoring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

// Metric name parts.
const (
	namespace = "db"
	exporter  = "exporter"
)

// Metric describes labels, type, and other information of a metric.
type Metric struct {
	Context          string                       `yaml:"context"`
	Labels           []string                     `yaml:"labels"`
	MetricsDesc      map[string]string            `yaml:"metricsdesc"`
	MetricsType      map[string]string            `yaml:"metricstype"`
	MetricsBuckets   map[string]map[string]string `yaml:"metricsbuckets"`
	FieldToAppend    string                       `yaml:"fieldtoappend"`
	Request          string                       `yaml:"request"`
	IgnoreZeroResult bool                         `yaml:"ignorezeroresult"`
}

// Metrics are used to load multiple metrics from file.
type Metrics struct {
	Metric []Metric `yaml:"metric"`
}

// Metrics to scrap. Use external file (default-metrics.toml and customize if provided).
var (
	metricsToScrap    Metrics
	additionalMetrics Metrics
	hashMap           map[int][]byte
	queryTimeout      = "5"
)

// Exporter collects Oracle DB metrics. It implements prometheus.Collector.
type Exporter struct {
	duration, error    prometheus.Gauge
	totalScrapes       prometheus.Counter
	scrapeErrors       *prometheus.CounterVec
	up                 prometheus.Gauge
	dbdClient          dbdpb.DatabaseDaemonClient
	closeConn          func() error
	customMetrics      string
	defaultFileMetrics string
	dbservice          string
	dbport             int
}

// getEnv returns the value of an environment variable, or returns the provided fallback value.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func atoi(stringValue string) int {
	intValue, err := strconv.Atoi(stringValue)
	if err != nil {
		klog.Fatalf("error while converting to int: %v", err)
		panic(err)
	}
	return intValue
}

func createDBDClient(ctx context.Context, service string, port int) (dbdpb.DatabaseDaemonClient, func() error, error) {
	klog.InfoS("connecting to DB:%s, port: %d", service, port)
	conn, err := common.DatabaseDaemonDialService(ctx, fmt.Sprintf("%s:%d", service, port), grpc.WithBlock())
	if err != nil {
		return nil, func() error { return nil }, err
	}
	return dbdpb.NewDatabaseDaemonClient(conn), conn.Close, nil
}

// NewExporter returns a new Oracle DB exporter for the provided DSN.
func NewExporter(ctx context.Context, defaultFileMetrics, customMetrics, service string, port int, qt string) (*Exporter, error) {
	// Load default and custom metrics.
	hashMap = make(map[int][]byte)
	if qt != "" {
		queryTimeout = qt
	}
	reloadMetrics(defaultFileMetrics, customMetrics)
	dbdClient, closeConn, err := createDBDClient(ctx, service, port)
	if err != nil {
		return nil, err
	}

	return &Exporter{
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_duration_seconds",
			Help:      "Duration of the last scrape of metrics from Oracle DB.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrapes_total",
			Help:      "Total number of times Oracle DB was scraped for metrics.",
		}),
		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrape_errors_total",
			Help:      "Total number of times an error occurred scraping an Oracle database.",
		}, []string{"collector"}),
		error: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_error",
			Help:      "Whether the last scrape of metrics from Oracle DB resulted in an error (1 for error, 0 for success).",
		}),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Whether the Oracle database server is up.",
		}),
		dbdClient:          dbdClient,
		closeConn:          closeConn,
		defaultFileMetrics: defaultFileMetrics,
		customMetrics:      customMetrics,
		dbservice:          service,
		dbport:             port,
	}, nil
}

// Describe describes all the metrics exported by the Oracle DB exporter.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	// We cannot know in advance what metrics the exporter will generate
	// So we use the poor man's describe method: Run a collect
	// and send the descriptors of all the collected metrics. The problem
	// here is that we need to connect to the Oracle DB. If it is currently
	// unavailable, the descriptors will be incomplete. Since this is a
	// stand-alone exporter and not used as a library within other code
	// implementing additional metrics, the worst that can happen is that we
	// don't detect inconsistent metrics created by this exporter itself.
	// Also, a change in the monitored Oracle instance may change the
	// exported metrics during the runtime of the exporter.

	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh

}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.error
	e.scrapeErrors.Collect(ch)
	ch <- e.up
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	e.totalScrapes.Inc()
	var err error
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
		if err == nil {
			e.error.Set(0)
		} else {
			e.error.Set(1)
		}
	}(time.Now())

	if _, err = e.dbdClient.RunSQLPlus(context.Background(), &dbdpb.RunSQLPlusCMDRequest{Commands: []string{"select sysdate from dual"}}); err != nil {
		if strings.Contains(err.Error(), "ORA-") {
			klog.Infoln("Reconnecting to DB")
			e.dbdClient, e.closeConn, err = createDBDClient(context.Background(), e.dbservice, e.dbport)
			if err != nil {
				klog.Errorln("Error pinging oracle:", err)
				e.closeConn()
				e.up.Set(0)
				return
			}
		}
	} else {
		klog.Infoln("Successfully pinged Oracle database: ")
		e.up.Set(1)
	}

	if checkIfMetricsChanged(e.defaultFileMetrics, e.customMetrics) {
		reloadMetrics(e.defaultFileMetrics, e.customMetrics)
	}

	wg := sync.WaitGroup{}

	for _, metric := range metricsToScrap.Metric {
		wg.Add(1)
		metric := metric //https://golang.org/doc/faq#closures_and_goroutines

		go func() {
			defer wg.Done()

			if len(metric.Request) == 0 {
				klog.Errorln("Error scraping for ", metric.MetricsDesc, ". Did you forget to define request in your toml file?")
				return
			}

			if len(metric.MetricsDesc) == 0 {
				klog.Errorln("Error scraping for query", metric.Request, ". Did you forget to define metrics desc in your toml file?")
				return
			}

			for column, metricType := range metric.MetricsType {
				if metricType == "histogram" {
					_, ok := metric.MetricsBuckets[column]
					if !ok {
						klog.Errorln("Unable to find MetricsBuckets configuration key for metric. (metric=" + column + ")")
						return
					}
				}
			}

			scrapeStart := time.Now()
			if err = ScrapeMetric(e.dbdClient, ch, metric); err != nil {
				klog.Errorln("Error scraping for", metric.Context, "_", metric.MetricsDesc, ":", err)
				e.scrapeErrors.WithLabelValues(metric.Context).Inc()
			} else {
				klog.Infoln("Successfully scraped metric: ", metric.Context, metric.MetricsDesc, time.Since(scrapeStart))
			}
		}()
	}
	wg.Wait()
}

// GetMetricType returns the prometheus type of a metric.
func GetMetricType(metricType string, metricsType map[string]string) prometheus.ValueType {
	var strToPromType = map[string]prometheus.ValueType{
		"gauge":     prometheus.GaugeValue,
		"counter":   prometheus.CounterValue,
		"histogram": prometheus.UntypedValue,
	}

	strType, ok := metricsType[strings.ToLower(metricType)]
	if !ok {
		return prometheus.GaugeValue
	}
	valueType, ok := strToPromType[strings.ToLower(strType)]
	if !ok {
		panic(errors.New("Error while getting prometheus type " + strings.ToLower(strType)))
	}
	return valueType
}

// ScrapeMetric calls ScrapeGenericValues using Metric struct values.
func ScrapeMetric(dbdClient dbdpb.DatabaseDaemonClient, ch chan<- prometheus.Metric, metricDefinition Metric) error {
	klog.InfoS("Calling function ScrapeGenericValues(): %v", metricDefinition)
	return ScrapeGenericValues(dbdClient, ch, metricDefinition.Context, metricDefinition.Labels,
		metricDefinition.MetricsDesc, metricDefinition.MetricsType, metricDefinition.MetricsBuckets,
		metricDefinition.FieldToAppend, metricDefinition.IgnoreZeroResult,
		metricDefinition.Request)
}

// ScrapeGenericValues is a generic method for retrieving metrics.
func ScrapeGenericValues(dbdClient dbdpb.DatabaseDaemonClient, ch chan<- prometheus.Metric, context string, labels []string,
	metricsDesc map[string]string, metricsType map[string]string, metricsBuckets map[string]map[string]string, fieldToAppend string, ignoreZeroResult bool, request string) error {
	metricsCount := 0
	genericParser := func(row map[string]string) error {
		// Construct labels and values.
		labelsValues := []string{}
		for _, label := range labels {
			labelsValues = append(labelsValues, row[label])
		}
		// Construct Prometheus values to sent back.
		for metric, metricHelp := range metricsDesc {
			value, err := strconv.ParseFloat(strings.TrimSpace(row[strings.ToUpper(metric)]), 64)
			// If not a float, skip current metric.
			if err != nil {
				klog.Errorln("Unable to convert current value to float (metric=" + metric +
					",metricHelp=" + metricHelp + ",value=<" + row[strings.ToUpper(metric)] + ">)")
				continue
			}
			// If metric does not use a field, content in metric's name.
			if strings.Compare(fieldToAppend, "") == 0 {
				desc := prometheus.NewDesc(
					prometheus.BuildFQName(namespace, context, metric),
					metricHelp,
					labels, nil,
				)
				if metricsType[strings.ToLower(metric)] == "histogram" {
					count, err := strconv.ParseUint(strings.TrimSpace(row["count"]), 10, 64)
					if err != nil {
						klog.Errorln("Unable to convert count value to int (metric=" + metric +
							",metricHelp=" + metricHelp + ",value=<" + row["count"] + ">)")
						continue
					}
					buckets := make(map[float64]uint64)
					for field, le := range metricsBuckets[metric] {
						lelimit, err := strconv.ParseFloat(strings.TrimSpace(le), 64)
						if err != nil {
							klog.Errorln("Unable to convert bucket limit value to float (metric=" + metric +
								",metricHelp=" + metricHelp + ",bucketlimit=<" + le + ">)")
							continue
						}
						counter, err := strconv.ParseUint(strings.TrimSpace(row[field]), 10, 64)
						if err != nil {
							klog.Errorln("Unable to convert ", field, " value to int (metric="+metric+
								",metricHelp="+metricHelp+",value=<"+row[field]+">)")
							continue
						}
						buckets[lelimit] = counter
					}
					ch <- prometheus.MustNewConstHistogram(desc, count, value, buckets, labelsValues...)
				} else {
					ch <- prometheus.MustNewConstMetric(desc, GetMetricType(metric, metricsType), value, labelsValues...)
				}
				// If no labels, use metric name
			} else {
				desc := prometheus.NewDesc(
					prometheus.BuildFQName(namespace, context, cleanName(row[strings.ToUpper(fieldToAppend)])),
					metricHelp,
					nil, nil,
				)
				if metricsType[strings.ToLower(metric)] == "histogram" {
					count, err := strconv.ParseUint(strings.TrimSpace(row["count"]), 10, 64)
					if err != nil {
						klog.Errorln("Unable to convert count value to int (metric=" + metric +
							",metricHelp=" + metricHelp + ",value=<" + row["count"] + ">)")
						continue
					}
					buckets := make(map[float64]uint64)
					for field, le := range metricsBuckets[metric] {
						lelimit, err := strconv.ParseFloat(strings.TrimSpace(le), 64)
						if err != nil {
							klog.Errorln("Unable to convert bucket limit value to float (metric=" + metric +
								",metricHelp=" + metricHelp + ",bucketlimit=<" + le + ">)")
							continue
						}
						counter, err := strconv.ParseUint(strings.TrimSpace(row[field]), 10, 64)
						if err != nil {
							klog.Errorln("Unable to convert ", field, " value to int (metric="+metric+
								",metricHelp="+metricHelp+",value=<"+row[field]+">)")
							continue
						}
						buckets[lelimit] = counter
					}
					ch <- prometheus.MustNewConstHistogram(desc, count, value, buckets)
				} else {
					ch <- prometheus.MustNewConstMetric(desc, GetMetricType(metric, metricsType), value)
				}
			}
			metricsCount++
		}
		return nil
	}
	err := GeneratePrometheusMetrics(dbdClient, genericParser, request)
	klog.Errorln("ScrapeGenericValues() - metricsCount: ", metricsCount)
	if err != nil {
		return err
	}
	if !ignoreZeroResult && metricsCount == 0 {
		return errors.New("No metrics found while parsing")
	}
	return err
}

// ParseSQLResponse parses the JSON result-set (returned by runSQLPlus API) and
// returns a list of rows with column-value mapping.
func ParseSQLResponse(resp *dbdpb.RunCMDResponse) ([]map[string]string, error) {
	var rows []map[string]string
	for _, msg := range resp.GetMsg() {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(msg), &row); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %v", msg, err)
		}

		rows = append(rows, row)
	}
	return rows, nil
}

// GeneratePrometheusMetrics parses metric query SQL results.
// Inspired by https://kylewbanks.com/blog/query-result-to-map-in-golang
func GeneratePrometheusMetrics(dbdClient dbdpb.DatabaseDaemonClient, parse func(row map[string]string) error, query string) error {

	// Add a timeout.
	timeout, err := strconv.Atoi(queryTimeout)
	if err != nil {
		klog.Fatal("error while converting timeout option value: ", err)
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	resp, err := dbdClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{query}, Suppress: true, Quiet: true})
	if err != nil {
		return err
	}

	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("Oracle query timed out")
	}
	rows, err := ParseSQLResponse(resp)
	if err != nil {
		return err
	}
	for _, r := range rows {
		// Call function to parse row.
		if err := parse(r); err != nil {
			return err
		}
	}
	return nil

}

// Oracle gives back names with special characters.
// This function cleans things up for Prometheus.
func cleanName(s string) string {
	s = strings.Replace(s, " ", "_", -1) // Remove spaces
	s = strings.Replace(s, "(", "", -1)  // Remove open parenthesis
	s = strings.Replace(s, ")", "", -1)  // Remove close parenthesis
	s = strings.Replace(s, "/", "", -1)  // Remove forward slashes
	s = strings.Replace(s, "*", "", -1)  // Remove asterisks
	s = strings.ToLower(s)
	return s
}

func hashFile(h hash.Hash, fn string) error {
	f, err := os.Open(fn)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	return nil
}

func checkIfMetricsChanged(defaultFileMetrics, customMetrics string) bool {
	for i, _customMetrics := range strings.Split(customMetrics, ",") {
		if len(_customMetrics) == 0 {
			continue
		}
		klog.Info("Checking modifications in following metrics definition file:", _customMetrics)
		h := sha256.New()
		if err := hashFile(h, _customMetrics); err != nil {
			klog.Errorln("Unable to get file hash", err)
			return false
		}
		// If any of files has been changed, reload metrics.
		if !bytes.Equal(hashMap[i], h.Sum(nil)) {
			klog.Infoln(_customMetrics, "has been changed. Reloading metrics...")
			hashMap[i] = h.Sum(nil)
			return true
		}
	}
	return false
}

func reloadMetrics(defaultFileMetrics, customMetrics string) {
	// Truncate metricsToScrap.
	metricsToScrap.Metric = []Metric{}

	defYAMLFile, err := ioutil.ReadFile(defaultFileMetrics)
	if err != nil {
		klog.Errorln(err)
		panic(errors.New("Error while loading " + defaultFileMetrics))
	}

	// Load default metrics.
	if err := yaml.Unmarshal(defYAMLFile, &metricsToScrap); err != nil {
		klog.Errorln(err)
	} else {
		klog.Infoln("Successfully loaded default metrics from: " + defaultFileMetrics)
	}

	// Load custom metrics.
	if strings.Compare(customMetrics, "") != 0 {
		for _, _customMetrics := range strings.Split(customMetrics, ",") {
			cusYAMLFile, err := ioutil.ReadFile(_customMetrics)
			if err != nil {
				klog.Errorln(err)
				panic(errors.New("Error while loading " + defaultFileMetrics))
			}
			if err := yaml.Unmarshal(cusYAMLFile, &additionalMetrics); err != nil {
				klog.Errorln(err)
			} else {
				klog.Infoln("Successfully loaded custom metrics from: " + _customMetrics)
			}
			metricsToScrap.Metric = append(metricsToScrap.Metric, additionalMetrics.Metric...)
		}
	} else {
		klog.Infoln("No custom metrics defined.")
	}
}
