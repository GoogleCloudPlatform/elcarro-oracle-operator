package monitoring

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

var (
	listenAddress = flag.String("web.listen-address", ":9187", "address:port to serve metrics on.")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "http path to serve metrics on.")
)

type logWrapper struct {
	log logr.Logger
}

func (lw *logWrapper) Println(s ...interface{}) {
	lw.log.Info(fmt.Sprintln(s...))
}

// ReadConfig all agents should use ReadConfig or StartExporting which uses
// this function to ensure their MetricSet is parsed correctly and validated.
func ReadConfig(config []byte) ([]MetricSet, error) {
	ms := []MetricSet{}
	if err := yaml.Unmarshal(config, &ms); err != nil {
		return nil, err
	}

	// Validate and transform config values
	for i := 0; i < len(ms); i++ {
		if err := validPromName(ms[i].Name); err != nil {
			return nil, err
		}
		if err := validPromName(ms[i].Namespace); err != nil {
			return nil, err
		}

		foundNonLabel := false
		for j := 0; j < len(ms[i].Metrics); j++ {
			foundNonLabel = foundNonLabel || ms[i].Metrics[j].Usage != Label
			ms[i].Metrics[j].column = strings.ToLower(ms[i].Metrics[j].Name)
			if err := validPromName(ms[i].Metrics[j].Name); err != nil {
				return nil, err
			}
		}
		if !foundNonLabel {
			return nil, fmt.Errorf("MetricSet %s does not contain a reportable metric (only Labels found)", ms[i].Name)
		}
	}

	return ms, nil
}

// Return the DSN as specified by the DATA_SOURCE* env vars, reading the
// appropriate configmap files for username and password
func GetDefaultDSN(log logr.Logger) *url.URL {
	uriVar := os.Getenv("DATA_SOURCE_URI")
	if len(uriVar) == 0 {
		log.Error(errors.New("no DATA_SOURCE_URI specified"), "missing env variables")
		klog.Fatal()
	}
	uri, err := url.Parse(uriVar)
	if err != nil {
		log.Error(errors.New("invalid DATA_SOURCE_URI"), "invalid env variables", "URI", uriVar)
		klog.Fatal()
	}
	userFile := os.Getenv("DATA_SOURCE_USER_FILE")
	user, err := ioutil.ReadFile(userFile)
	if err != nil || len(user) == 0 {
		log.Error(err, "Error reading DATA_SOURCE_USER_FILE or invalid file")
	}
	passFile := os.Getenv("DATA_SOURCE_PASS_FILE")
	pass, err := ioutil.ReadFile(passFile)
	if err != nil || len(pass) == 0 {
		log.Error(err, "Error reading DATA_SOURCE_PASS_FILE or invalid file")
	}
	uri.User = url.UserPassword(string(user), string(pass))
	return uri
}

func StartExporting(log logr.Logger, reg *prometheus.Registry, db DBFactory, configFiles []string, extraLabels map[string]string) {
	var ms []MetricSet
	for _, c := range configFiles {
		log.Info("Loading config", "path", c)
		data, err := ioutil.ReadFile(c)
		if err != nil {
			log.Error(err, "failed to read config", "configFile", c)
			klog.Fatal()
		}
		conf, err := ReadConfig(data)
		if err != nil {
			log.Error(err, "failed to parse config", "configFile", c)
			klog.Fatal()
		}
		ms = append(ms, conf...)
	}

	mon := NewMonitor(log, db, ms)
	prometheus.WrapRegistererWith(extraLabels, reg).MustRegister(mon)

	// See more info on
	// https://github.com/prometheus/client_golang/blob/master/prometheus/promhttp/http.go#L269
	// This is a best effort monitor that will retry connections when the
	// database is offline, we dont want the container to be restarted and
	// end up in crash loop backoff.
	opts := promhttp.HandlerOpts{
		Registry:      reg,
		ErrorLog:      &logWrapper{log},
		ErrorHandling: promhttp.ContinueOnError,
	}
	http.Handle(*metricsPath, promhttp.HandlerFor(reg, opts))
	srv := &http.Server{Addr: *listenAddress}
	log.Info("Starting monitoring agent", "address", *listenAddress, "path", *metricsPath)
	if err := srv.ListenAndServe(); err != nil {
		log.Error(err, "HTTP Server shutdown failed")
		klog.Fatal()
	}
}

// StartExportingContainerMetrics starts an metric exporter for cgroup and disk
// stats. If you want to export these metrics on an existing '/metrics'
// endpoint instead register the NewDBContainerMonitor directly to your
// prometheus registry.
func StartExportingContainerMetrics(log logr.Logger, reg *prometheus.Registry, volumes []VolumeInfo, extraLabels map[string]string) error {
	prometheus.WrapRegistererWith(extraLabels, reg).MustRegister(NewDBContainerMonitor(log, volumes))

	// See more info on
	// https://github.com/prometheus/client_golang/blob/master/prometheus/promhttp/http.go#L269
	// This is a best effort monitor that will retry connections when the
	// database is offline, we dont want the container to be restarted and
	// end up in crash loop backoff.
	opts := promhttp.HandlerOpts{
		Registry:      reg,
		ErrorLog:      &logWrapper{log},
		ErrorHandling: promhttp.ContinueOnError,
	}
	mux := http.NewServeMux()
	mux.Handle(*metricsPath, promhttp.HandlerFor(reg, opts))
	log.Info("Starting container metrics server", "address", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, mux); err != nil {
		log.Error(err, "HTTP Server shutdown failed")
		return err
	}
	return nil
}
