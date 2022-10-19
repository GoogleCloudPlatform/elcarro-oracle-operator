package main

import (
	"database/sql"
	"flag"
	"fmt"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/monitoring"
	_ "github.com/godror/godror"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
)

type godrorFactory struct {
	dsn string
}

func (g godrorFactory) Open() (*sql.DB, error) {
	db, err := sql.Open("godror", g.dsn)
	if err != nil {
		err = fmt.Errorf("DB open failed: %w", err)
	}
	return db, err
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Avoid adding unexpected metrics by using a custom registry.
	registry := prometheus.NewRegistry()
	log := klog.NewKlogr()
	db := godrorFactory{dsn: monitoring.GetDefaultDSN(log).String()}
	monitoring.StartExporting(
		log,
		registry,
		db,
		[]string{"/oracle_metrics.yaml",
			"/oracle_unified_metrics.yaml"},
		nil,
	)
	log.Info("Shutting down")
}
