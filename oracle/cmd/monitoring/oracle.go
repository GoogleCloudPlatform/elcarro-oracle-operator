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

var dbSingleton *sql.DB

func (g godrorFactory) Open() (*sql.DB, error) {
	if dbSingleton == nil || dbSingleton.Ping() != nil {
		if dbSingleton != nil {
			dbSingleton.Close()
		}
		db, err := sql.Open("godror", g.dsn)
		if err != nil {
			return nil, fmt.Errorf("DB open failed: %w", err)
		}
		dbSingleton = db
	}
	return dbSingleton, nil
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
