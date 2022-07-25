package pitr

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type fakeStorageClient struct {
	hashFun   func(ctx context.Context, path string) (h []byte, retErr error)
	mtimeFun  func(ctx context.Context, path string) (time.Time, error)
	mkdirpFun func(ctx context.Context, path string, mode os.FileMode) error
	readFun   func(ctx context.Context, path string) (io.ReadCloser, error)
	writeFun  func(ctx context.Context, path string) (io.WriteCloser, error)
	deleteFun func(ctx context.Context, path string, ignoreNotExists bool) error
	closeFun  func(ctx context.Context) error
}

func (f *fakeStorageClient) hash(ctx context.Context, path string) (h []byte, retErr error) {
	if f.hashFun != nil {
		return f.hashFun(ctx, path)
	}
	panic("not mocked")
}

func (f *fakeStorageClient) mtime(ctx context.Context, path string) (time.Time, error) {
	if f.mtimeFun != nil {
		return f.mtimeFun(ctx, path)
	}
	panic("not mocked")
}

func (f *fakeStorageClient) mkdirp(ctx context.Context, path string, mode os.FileMode) error {
	if f.mkdirpFun != nil {
		return f.mkdirpFun(ctx, path, mode)
	}
	panic("not mocked")
}

func (f *fakeStorageClient) read(ctx context.Context, path string) (io.ReadCloser, error) {
	if f.readFun != nil {
		return f.readFun(ctx, path)
	}
	panic("not mocked")
}

func (f *fakeStorageClient) write(ctx context.Context, path string) (io.WriteCloser, error) {
	if f.writeFun != nil {
		return f.writeFun(ctx, path)
	}
	panic("not mocked")
}

func (f *fakeStorageClient) delete(ctx context.Context, path string, ignoreNotExists bool) error {
	if f.deleteFun != nil {
		return f.deleteFun(ctx, path, ignoreNotExists)
	}
	panic("not mocked")
}

func (f *fakeStorageClient) close(ctx context.Context) error {
	if f.closeFun != nil {
		return f.closeFun(ctx)
	}
	panic("not mocked")
}

type fakeServer struct {
	*dbdpb.UnimplementedDatabaseDaemonServer
	runSQLPlus          func(context.Context, *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
	runSQLPlusFormatted func(context.Context, *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
}

func (f *fakeServer) RunSQLPlus(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.runSQLPlus != nil {
		return f.runSQLPlus(ctx, req)
	}
	panic("not mocked")
}

func (f *fakeServer) RunSQLPlusFormatted(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.runSQLPlusFormatted != nil {
		return f.runSQLPlusFormatted(ctx, req)
	}
	panic("not mocked")
}

func newFakeDatabaseDaemonClient(t *testing.T, server *fakeServer) (dbdpb.DatabaseDaemonClient, func()) {
	t.Helper()
	grpcSvr := grpc.NewServer()

	dbdpb.RegisterDatabaseDaemonServer(grpcSvr, server)
	lis := bufconn.Listen(2 * 1024 * 1024)
	go grpcSvr.Serve(lis)

	dbdConn, err := grpc.Dial("test",
		grpc.WithInsecure(),
		grpc.WithContextDialer(
			func(ctx context.Context, s string) (conn net.Conn, err error) {
				return lis.Dial()
			}),
	)
	if err != nil {
		t.Fatalf("failed to dial to dbDaemon: %v", err)
	}
	return dbdpb.NewDatabaseDaemonClient(dbdConn), func() {
		dbdConn.Close()
		grpcSvr.GracefulStop()
	}
}

func TestMerge(t *testing.T) {
	testCases := []struct {
		name string
		data LogMetadata
		want [][]string
	}{
		{
			name: "empty entry",
		},
		{
			name: "one entry not replicated",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
					},
				},
			},
		},
		{
			name: "one entry",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log1",
						},
					},
				},
			},
			want: [][]string{{"1-2-1", "1-2-1"}},
		},
		{
			name: "two consecutive entries",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log1",
						},
					},
					"1-2-2": {
						FirstTime: time.Unix(1628282000, 0),
						NextTime:  time.Unix(1628283000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log2",
						},
					},
				},
			},
			want: [][]string{{"1-2-1", "1-2-2"}},
		},
		{
			name: "multiple consecutive entries",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log1",
						},
					},
					"1-2-2": {
						FirstTime: time.Unix(1628282000, 0),
						NextTime:  time.Unix(1628283000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log2",
						},
					},
					"1-2-3": {
						FirstTime: time.Unix(1628283000, 0),
						NextTime:  time.Unix(1628284000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log3",
						},
					},
					"1-2-4": {
						FirstTime: time.Unix(1628284000, 0),
						NextTime:  time.Unix(1628285000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log4",
						},
					},
				},
			},
			want: [][]string{{"1-2-1", "1-2-4"}},
		},
		{
			name: "multiple entries with no merged ranges",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log1",
						},
					},
					"1-2-2": {
						FirstTime: time.Unix(1628282000, 0),
						NextTime:  time.Unix(1628283000, 0),
					},
					"1-2-3": {
						FirstTime: time.Unix(1628283000, 0),
						NextTime:  time.Unix(1628284000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log3",
						},
					},
					"1-2-4": {
						FirstTime: time.Unix(1628284000, 0),
						NextTime:  time.Unix(1628285000, 0),
					},
					"1-2-5": {
						FirstTime: time.Unix(1628285000, 0),
						NextTime:  time.Unix(1628286000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log5",
						},
					},
					"1-2-7": {
						FirstTime: time.Unix(1628287000, 0),
						NextTime:  time.Unix(1628288000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log7",
						},
					},
					"1-2-8": {
						FirstTime: time.Unix(1628288000, 0),
						NextTime:  time.Unix(1628289000, 0),
					},
				},
			},
			want: [][]string{
				{"1-2-1", "1-2-1"},
				{"1-2-3", "1-2-3"},
				{"1-2-5", "1-2-5"},
				{"1-2-7", "1-2-7"},
			},
		},

		{
			name: "multiple entries with two merged ranges",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log1",
						},
					},
					"1-2-2": {
						FirstTime: time.Unix(1628282000, 0),
						NextTime:  time.Unix(1628283000, 0),
					},
					"1-2-3": {
						FirstTime: time.Unix(1628283000, 0),
						NextTime:  time.Unix(1628284000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log3",
						},
					},
					"1-2-4": {
						FirstTime: time.Unix(1628284000, 0),
						NextTime:  time.Unix(1628285000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log4",
						},
					},
					"1-2-5": {
						FirstTime: time.Unix(1628285000, 0),
						NextTime:  time.Unix(1628286000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log5",
						},
					},
				},
			},
			want: [][]string{
				{"1-2-1", "1-2-1"},
				{"1-2-3", "1-2-5"},
			},
		},
		{
			name: "multiple entries with three merged ranges",
			data: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						FirstTime: time.Unix(1628281000, 0),
						NextTime:  time.Unix(1628282000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log1",
						},
					},
					"1-2-2": {
						FirstTime: time.Unix(1628282000, 0),
						NextTime:  time.Unix(1628283000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log2",
						},
					},
					"1-2-3": {
						FirstTime: time.Unix(1628283000, 0),
						NextTime:  time.Unix(1628284000, 0),
					},
					"1-2-4": {
						FirstTime: time.Unix(1628284000, 0),
						NextTime:  time.Unix(1628285000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log4",
						},
					},
					"1-2-5": {
						FirstTime: time.Unix(1628285000, 0),
						NextTime:  time.Unix(1628286000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log5",
						},
					},
					"1-2-6": {
						FirstTime: time.Unix(1628286000, 0),
						NextTime:  time.Unix(1628287000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log6",
						},
					},
					"1-2-7": {
						FirstTime: time.Unix(1628287000, 0),
						NextTime:  time.Unix(1628288000, 0),
					},
					"1-2-8": {
						FirstTime: time.Unix(1628288000, 0),
						NextTime:  time.Unix(1628289000, 0),
						LogHashEntry: LogHashEntry{
							ReplicaPath: "log8",
						},
					},
				},
			},
			want: [][]string{
				{"1-2-1", "1-2-2"},
				{"1-2-4", "1-2-6"},
				{"1-2-8", "1-2-8"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(tc.data)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Merge got unexpected result: want-, got+: %s\n", diff)
			}
		})
	}
}

func TestCleanUpLogs(t *testing.T) {
	testDir, err := ioutil.TempDir("", "TestCleanUpLogs")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	defer os.RemoveAll(testDir)
	ctx := context.Background()
	testCases := []struct {
		name          string
		now           time.Time
		retentionDays int
		metadata      LogMetadata
		wantDeleted   []string
		wantMetadata  LogMetadata
	}{
		{
			name:          "cleanupOneExpiredLog",
			now:           time.Date(2021, 9, 3, 12, 0, 0, 0, time.UTC),
			retentionDays: 1,
			metadata: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						NextTime: time.Date(2021, 9, 1, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						},
					},
					"1-2-2": {
						NextTime: time.Date(2021, 9, 2, 2, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						},
					},
				},
			},
			wantDeleted: []string{
				"gs://pitr/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
			},
			wantMetadata: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-2": {
						NextTime: time.Date(2021, 9, 2, 2, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						},
					},
				},
			},
		},
		{
			name:          "cleanupTwoExpiredLogs",
			now:           time.Date(2021, 9, 3, 12, 0, 0, 0, time.UTC),
			retentionDays: 2,
			metadata: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-1-1": {
						NextTime: time.Date(2021, 8, 30, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_08_30/o1_mf_1_1_imz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_08_30/o1_mf_1_1_imz1gbon_.arc",
						},
					},
					"1-1-2": {
						NextTime: time.Date(2021, 8, 31, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_08_31/o1_mf_1_2_imz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_08_31/o1_mf_1_2_imz1gbon_.arc",
						},
					},
					"1-2-1": {
						NextTime: time.Date(2021, 9, 1, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						},
					},
					"1-2-2": {
						NextTime: time.Date(2021, 9, 2, 2, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						},
					},
				},
			},
			wantDeleted: []string{
				"gs://pitr/archivelog/2021_08_30/o1_mf_1_1_imz1gbon_.arc",
				"gs://pitr/archivelog/2021_08_31/o1_mf_1_2_imz1gbon_.arc",
			},
			wantMetadata: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-2-1": {
						NextTime: time.Date(2021, 9, 1, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						},
					},
					"1-2-2": {
						NextTime: time.Date(2021, 9, 2, 2, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_02/o1_mf_1_2_jmz1gbon_.arc",
						},
					},
				},
			},
		},
		{
			name:          "skipIfNoExpectedDateInLogPath",
			now:           time.Date(2021, 9, 3, 12, 0, 0, 0, time.UTC),
			retentionDays: 2,
			metadata: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-1-1": {
						NextTime: time.Date(2021, 8, 30, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/o1_mf_1_1_imz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/o1_mf_1_1_imz1gbon_.arc",
						},
					},
					"1-1-2": {
						NextTime: time.Date(2021, 8, 31, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_08_31/o1_mf_1_2_imz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "",
						},
					},
					"1-2-1": {
						NextTime: time.Date(2021, 9, 1, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						},
					},
				},
			},
			wantMetadata: LogMetadata{
				KeyToLogEntry: map[string]LogMetadataEntry{
					"1-1-1": {
						NextTime: time.Date(2021, 8, 30, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/o1_mf_1_1_imz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/o1_mf_1_1_imz1gbon_.arc",
						},
					},
					"1-1-2": {
						NextTime: time.Date(2021, 8, 31, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_08_31/o1_mf_1_2_imz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "",
						},
					},
					"1-2-1": {
						NextTime: time.Date(2021, 9, 1, 1, 0, 0, 0, time.UTC),
						SrcPath:  "/u03/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						LogHashEntry: LogHashEntry{
							ReplicaPath: "gs://pitr/archivelog/2021_09_01/o1_mf_1_1_jmz1gbon_.arc",
						},
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			metadataPath := filepath.Join(testDir, tc.name, "metadata")
			metadataStore, err := NewSimpleStore(ctx, metadataPath)
			if err != nil {
				t.Fatal(err, "failed to create a metadata store")
			}

			hashStore, err := NewSimpleStore(ctx, filepath.Join(testDir, tc.name, "hash"))
			if err != nil {
				t.Fatal(err, "failed to create a hash store")
			}
			// add mock data in stores
			if err := metadataStore.write(ctx, MetadataStorePath, tc.metadata); err != nil {
				t.Fatal(err, "failed to add mock data into the metadata store")
			}
			for _, entry := range tc.metadata.KeyToLogEntry {
				if err := hashStore.write(ctx, entry.SrcPath, LogHashEntry{
					ReplicaPath: entry.ReplicaPath,
				}); err != nil {
					t.Fatal(err, "failed to add mock data into the hash store")
				}
			}
			var gotDeleted []string
			sc := &fakeStorageClient{
				deleteFun: func(ctx context.Context, path string, ignoreNotExists bool) error {
					gotDeleted = append(gotDeleted, path)
					return nil
				},
				closeFun: func(ctx context.Context) error {
					return nil
				},
			}
			timeNow = func() time.Time {
				return tc.now
			}

			if err := cleanUpLogs(ctx, sc, tc.retentionDays, metadataStore, hashStore); err != nil {
				t.Fatalf("cleanUpLogs failed: %v", err)
			}

			// verify storage client deleted expected files
			if diff := cmp.Diff(tc.wantDeleted, gotDeleted, cmpopts.SortSlices(func(s1, s2 string) bool { return s1 < s2 })); diff != "" {
				t.Errorf("cleanUpLogs deleted unexpected files: want-, got+: %s\n", diff)
			}

			// verify metadata updated as expected
			gotMetadata := &LogMetadata{}
			if err := metadataStore.Read(ctx, MetadataStorePath, gotMetadata); err != nil {
				t.Fatalf("read metadata after cleanUp failed: %v", err)
			}
			if diff := cmp.Diff(tc.wantMetadata, *gotMetadata); diff != "" {
				t.Errorf("cleanUpLogs got unexpected metadata: want-, got+: %s\n", diff)
			}

			// verify hash updated as expected
			for k, entry := range tc.metadata.KeyToLogEntry {
				if _, ok := tc.wantMetadata.KeyToLogEntry[k]; ok {
					if err := hashStore.Read(ctx, entry.SrcPath, &LogMetadataEntry{}); err != nil {
						t.Errorf("hashStore read %s failed: %v", entry.SrcPath, err)
					}
				} else {
					if err := hashStore.Read(ctx, entry.SrcPath, &LogMetadataEntry{}); err != nil {
						t.Errorf("hashStore read %s got nil, want not-nil error", entry.SrcPath)
					}
				}
			}
		})
	}
}

func TestSetArchiveLag(t *testing.T) {
	s := &fakeServer{}
	dbdClient, cleanup := newFakeDatabaseDaemonClient(t, s)
	defer cleanup()
	ctx := context.Background()

	testCases := []struct {
		name        string
		existingLag string
		wantSQLs    []string
	}{
		{
			name:        "update to default value",
			existingLag: "0",
			wantSQLs:    []string{"alter system set archive_lag_target=600 scope=both"},
		},
		{
			name:        "skip update",
			existingLag: "900",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s.runSQLPlusFormatted = func(ctx context.Context, request *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				return &dbdpb.RunCMDResponse{Msg: []string{fmt.Sprintf(`{"VALUE":"%s"}`, tc.existingLag)}}, nil
			}
			var gotSQLs []string
			s.runSQLPlus = func(ctx context.Context, request *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				gotSQLs = append(gotSQLs, request.GetCommands()...)
				return &dbdpb.RunCMDResponse{}, nil
			}

			if err := SetArchiveLag(ctx, dbdClient); err != nil {
				t.Fatalf("SetArchiveLag failed: %v", err)
			}

			if diff := cmp.Diff(tc.wantSQLs, gotSQLs); diff != "" {
				t.Errorf("SetArchiveLag got unexpected SQLs: want-, got+: %s\n", diff)
			}
		})
	}
}
