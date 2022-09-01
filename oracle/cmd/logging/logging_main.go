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

// contains a script to tail log files
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"
	"google.golang.org/grpc"

	dbdaemonlib "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	logTypeAlert    = "ALERT"
	logTypeListener = "LISTENER"

	alertLogPathQuery = `select value from v$diag_info where name = 'Diag Trace'`
	databaseNameQuery = `select name from v$database`

	listenerBaseVar = `ADR_BASE_SECURE`
)

var (
	logType      = flag.String("logType", "", "the log file to stream. Currently supports: ALERT, LISTENER")
	debugLogger  = flag.Bool("debugLogger", false, "enable to get debug logs from the logging sidecar")
	pollInterval = flag.Duration("pollInterval", 180*time.Second, "time interval to query for updates to log locations (total time to tail a new log might be 2x poll interval)")

	// If the listener directory becomes configurable then we will need to modify this
	listenerOraPath = filepath.Join(fmt.Sprintf(consts.ListenerDir, consts.DataMount), "SECURE/listener.ora")

	logger                *log.Logger
	latestLogFilePath     string
	latestLogFilePathLock sync.Mutex
	currentTail           *tailRoutine
)

func createDBDClient(ctx context.Context) (dbdpb.DatabaseDaemonClient, func() error, error) {
	conn, err := dbdaemonlib.DatabaseDaemonDialLocalhost(ctx, consts.DefaultDBDaemonPort, grpc.WithBlock())
	if err != nil {
		return nil, func() error { return nil }, err
	}
	return dbdpb.NewDatabaseDaemonClient(conn), conn.Close, nil
}

func main() {
	flag.Parse()

	logger = tail.DiscardingLogger
	if *debugLogger {
		logger = tail.DefaultLogger
	}

	if *logType != logTypeAlert && *logType != logTypeListener {
		logger.Fatalf("unrecognized log type: %v", *logType)
	}

	logger.Print("logging main class starting up")

	go pollForPathUpdates(context.Background(), *logType)
	createTailRoutine()
}

type tailRoutine struct {
	filePath string
	t        *tail.Tail
}

func (tr *tailRoutine) startTail() error {
	var err error
	tr.t, err = tail.TailFile(tr.filePath, tail.Config{Follow: true, ReOpen: true, Logger: logger})
	if err != nil {
		return err
	}

	go func() {
		for line := range tr.t.Lines {
			fmt.Println(line.Text)
		}
	}()
	return nil
}

func (tr *tailRoutine) stopTail() error {
	if err := tr.t.Stop(); err != nil {
		return err
	}
	tr.t.Cleanup()
	return nil
}

func createTailRoutine() {
	tick := time.NewTicker(*pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			latestLogFilePathLock.Lock()
			latestPath := latestLogFilePath
			latestLogFilePathLock.Unlock()

			if latestPath != "" && (currentTail == nil || currentTail.t.Filename != latestPath) {
				logger.Printf("new path found: %v", latestPath)
				if !fileExists(latestPath) {
					logger.Printf("file does not currently exist, waiting: %v", latestPath)
					continue
				}

				if currentTail != nil {
					if err := currentTail.stopTail(); err != nil {
						logger.Fatalf("Unable to stop tail err=%v", err)
					}
				}

				currentTail = &tailRoutine{
					filePath: latestPath,
				}

				if err := currentTail.startTail(); err != nil {
					logger.Fatalf("Unable to start tail err=%v", err)
				}
			}
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	} else if !os.IsNotExist(err) {
		logger.Fatalf("error checking file existence: %v", err)
	}
	return false
}

func getListenerLogBasePath(secureListenerPath string) (string, error) {
	f, err := os.Open(secureListenerPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, listenerBaseVar) {
			split := strings.Split(line, "=")
			if len(split) != 2 {
				return "", fmt.Errorf("got len(split)=%d, expected 2", len(split))
			}
			return strings.TrimSpace(split[1]), nil
		}
	}

	return "", fmt.Errorf("No %s line found", listenerBaseVar)
}

func pollForPathUpdates(ctx context.Context, logType string) {

	tick := time.NewTicker(*pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			var newLogFilePath string
			if logType == logTypeListener {
				listenerLogBase, err := getListenerLogBasePath(listenerOraPath)
				if err != nil {
					logger.Printf("unable to find base path for listener log: %v", err)
					continue
				}

				hostname, err := os.Hostname()
				if err != nil {
					logger.Fatalf("error getting hostname %v", err)
				}
				newLogFilePath = filepath.Join(listenerLogBase, "/diag/tnslsnr", hostname, "/secure/trace/secure.log")
			} else if logType == logTypeAlert {
				alertLogBase, err := queryDB(ctx, alertLogPathQuery)
				if err != nil || len(alertLogBase) == 0 {
					logger.Printf("Error querying alert log path err=%v, alertLogBase=%v", err, alertLogBase)
					continue
				}

				dbName, err := queryDB(ctx, databaseNameQuery)
				if err != nil || len(dbName) == 0 {
					logger.Printf("Error querying dbname err=%v, dbName=%v", err, dbName)
					continue
				}
				newLogFilePath = filepath.Join(alertLogBase, fmt.Sprintf("alert_%s.log", dbName))
			}

			latestLogFilePathLock.Lock()
			latestLogFilePath = newLogFilePath
			latestLogFilePathLock.Unlock()
		}
	}
}

func queryDB(ctx context.Context, query string) (string, error) {
	dbdClient, closeConn, err := createDBDClient(ctx)
	if err != nil {
		return "", err
	}
	defer closeConn()

	resp, err := dbdClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{query}, Suppress: false, Quiet: true})
	if err != nil {
		return "", err
	}

	row := make(map[string]string)
	if resp == nil || len(resp.GetMsg()) < 1 {
		return "", fmt.Errorf("query did not return any response, resp=%v", resp)
	}
	if err := json.Unmarshal([]byte(resp.GetMsg()[0]), &row); err != nil {
		return "", err
	}
	if len(row) > 1 {
		return "", fmt.Errorf("query returned more than one value, got=%d values", len(row))
	}

	var queryVal string
	for _, value := range row {
		queryVal = value
	}
	return queryVal, nil
}
