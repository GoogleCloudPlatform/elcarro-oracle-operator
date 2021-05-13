// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	//Required for debugging
	//_ "net/http/pprof"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/third_party/monitoring"
)

var (
	listenAddress      = flag.String("listen_address", ":9161", "Address to listen on for web interface and telemetry")
	metricPath         = flag.String("telemetry_path", "/metrics", "Path under which to expose metrics")
	defaultFileMetrics = flag.String("default_metrics", "default-metrics.yaml", "File with default metrics in a YAML file")
	queryTimeout       = flag.String("query_timeout", "5", "Query timeout in seconds")
	customMetrics      = flag.String("custom_metrics", "", "File that may contain various custom metrics in a YAML file")
	dbservice          = flag.String("dbservice", "", "The DB service.")
	dbport             = flag.Int("dbport", 0, "The DB service port.")
	initTimeoutMin     = flag.Int("init_timeout_min", 10, "The monitor agent initialization timeout in minutes, which includes the time to wait for the DB ready.")
	logFlushFreq       = 5 * time.Second
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	klog.InfoS("Starting oracledb_exporter ")
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*initTimeoutMin)*time.Minute)
	defer cancel()
	exporter, err := monitoring.NewExporter(ctx, *defaultFileMetrics, *customMetrics, *dbservice, *dbport, *queryTimeout)
	if err != nil {
		klog.ErrorS(err, "error in starting monitoring agent")
		os.Exit(1)
	}
	prometheus.MustRegister(exporter)
	klog.InfoS("new exporter registered")

	InitLogs()
	defer FlushLogs()

	opts := promhttp.HandlerOpts{
		ErrorLog:      NewLogger("monitor"),
		ErrorHandling: promhttp.ContinueOnError,
	}
	http.Handle(*metricPath, promhttp.HandlerFor(prometheus.DefaultGatherer, opts))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Oracle DB Exporter </title></head><body><h1>Oracle DB Exporter </h1><p><a href='" + *metricPath + "'>Metrics</a></p></body></html>"))
	})
	klog.InfoS("Listening on", *listenAddress)
	if err = http.ListenAndServe(*listenAddress, nil); err != nil {
		klog.ErrorS(err, "error in starting monitoring agent")
		os.Exit(1)
	}
}

// KlogWriter serves as a bridge between the standard log package and the glog package.
type KlogWriter struct{}

// Write implements the io.Writer interface.
func (writer KlogWriter) Write(data []byte) (n int, err error) {
	klog.InfoDepth(1, string(data))
	return len(data), nil
}

// InitLogs initializes logs the way we want for kubernetes.
func InitLogs() {
	log.SetOutput(KlogWriter{})
	log.SetFlags(0)
	// The default glog flush interval is 5 seconds.
	go wait.Forever(klog.Flush, logFlushFreq)
}

// FlushLogs flushes logs immediately.
func FlushLogs() {
	klog.Flush()
}

// NewLogger creates a new log.Logger which sends logs to klog.Info.
func NewLogger(prefix string) *log.Logger {
	return log.New(KlogWriter{}, prefix, 0)
}
