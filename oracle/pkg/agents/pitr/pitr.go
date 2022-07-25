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

package pitr

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"k8s.io/klog/v2"
)

var (
	syncInterval           = time.Minute
	syncTimeout            = time.Hour
	cleanupInterval        = time.Hour * 24
	castagnoli             = crc32.MakeTable(crc32.Castagnoli)
	replicationThreadCount = 4
	timeNow                = time.Now
)

const (
	gsPrefix = "gs://"
	// MetadataStorePath is the path to log metadata catalog.
	MetadataStorePath = "catalog"
	archiveLagParam   = "archive_lag_target"
	archiveLagVal     = 600 // 10 min
)

type storageClient interface {
	// hash returns CRC32C hash of a file.
	hash(ctx context.Context, path string) (h []byte, retErr error)

	// mtime returns the time that the data was last modified.
	mtime(ctx context.Context, path string) (time.Time, error)

	// mkdirp creates a directory named path, along with any necessary parents.
	mkdirp(ctx context.Context, path string, mode os.FileMode) error

	// read reads the named path and returns the reader.
	read(ctx context.Context, path string) (io.ReadCloser, error)

	// write writes the named path and returns the writer.
	write(ctx context.Context, path string) (io.WriteCloser, error)

	// delete deletes the named path.
	delete(ctx context.Context, path string, ignoreNotExists bool) error

	// close closes the client
	close(ctx context.Context) error
}

type srcDest struct {
	src  string
	dest string
}

type gcsClient struct {
	c *storage.Client
}

func (g *gcsClient) hash(ctx context.Context, path string) (h []byte, retErr error) {
	bucket, name, err := g.splitURI(path)
	if err != nil {
		return nil, err
	}

	c, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := c.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()

	b := c.Bucket(bucket)
	// check if bucket exists and it is accessible
	a, err := b.Object(name).Attrs(ctx)
	if err != nil {
		return nil, err
	}

	hashBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(hashBytes, a.CRC32C)
	return hashBytes, err
}

func (g *gcsClient) mtime(ctx context.Context, path string) (t time.Time, retErr error) {
	bucket, name, err := g.splitURI(path)
	if err != nil {
		return time.Time{}, err
	}

	b := g.c.Bucket(bucket)
	// check if bucket exists and it is accessible
	if _, err := b.Attrs(ctx); err != nil {
		return time.Time{}, err
	}

	r, err := b.Object(name).NewReader(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		if err := r.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()
	return r.Attrs.LastModified, nil
}

func (g *gcsClient) mkdirp(context.Context, string, os.FileMode) error {
	// https://cloud.google.com/storage/docs/folders
	return nil
}

func (g *gcsClient) read(ctx context.Context, path string) (closer io.ReadCloser, retErr error) {
	bucket, name, err := g.splitURI(path)
	if err != nil {
		return nil, err
	}

	b := g.c.Bucket(bucket)
	// check if bucket exists and it is accessible
	if _, err := b.Attrs(ctx); err != nil {
		return nil, err
	}

	r, err := b.Object(name).NewReader(ctx)
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (g *gcsClient) write(ctx context.Context, path string) (closer io.WriteCloser, retErr error) {
	bucket, name, err := g.splitURI(path)
	if err != nil {
		return nil, err
	}

	b := g.c.Bucket(bucket)
	// check if bucket exists and it is accessible
	if _, err := b.Attrs(ctx); err != nil {
		return nil, err
	}

	return b.Object(name).NewWriter(ctx), nil
}

func (g *gcsClient) delete(ctx context.Context, path string, ignoreNotExists bool) error {
	bucket, name, err := g.splitURI(path)
	if err != nil {
		return err
	}
	o := g.c.Bucket(bucket).Object(name)
	if err := o.Delete(ctx); err != nil && !(ignoreNotExists && err == storage.ErrObjectNotExist) {
		return fmt.Errorf("Bucket(%q).Object(%q).Delete: %w", bucket, name, err)
	}
	return nil
}

func (g *gcsClient) close(context.Context) error {
	return g.c.Close()
}

func (g *gcsClient) splitURI(url string) (bucket, name string, err error) {
	u := strings.TrimPrefix(url, gsPrefix)
	if u == url {
		return "", "", fmt.Errorf("URL %q is missing the %q prefix", url, gsPrefix)
	}
	if i := strings.Index(u, "/"); i >= 2 {
		return u[:i], u[i+1:], nil
	}
	return "", "", fmt.Errorf("URL %q does not specify a bucket and a name", url)
}

func newGcsClient(ctx context.Context) (*gcsClient, error) {
	c, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &gcsClient{c: c}, nil
}

type fsClient struct{}

func (f *fsClient) read(_ context.Context, path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (f *fsClient) write(_ context.Context, path string) (io.WriteCloser, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}

	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
}

func (f *fsClient) hash(_ context.Context, path string) (h []byte, retErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()

	hash := crc32.New(castagnoli)

	_, err = io.Copy(hash, file)
	if err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

func (f *fsClient) mkdirp(_ context.Context, path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (f *fsClient) mtime(_ context.Context, path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	// convert to string to avoid complexity in serialization
	t := info.ModTime()
	return t, nil
}

func (f *fsClient) delete(_ context.Context, path string, ignoreNotExists bool) error {
	if err := os.Remove(path); err != nil && !(ignoreNotExists && os.IsNotExist(err)) {
		return err
	}
	return nil
}

func (f *fsClient) close(context.Context) error {
	return nil
}

type replicationGroup struct {
	wg          *sync.WaitGroup
	errCount    uint64
	toReplicate <-chan srcDest
	srcClient   storageClient
	destClient  storageClient
}

func newReplicationGroup(toReplicate <-chan srcDest, srcClient storageClient, destClient storageClient) *replicationGroup {
	return &replicationGroup{
		wg:          &sync.WaitGroup{},
		errCount:    0,
		toReplicate: toReplicate,
		srcClient:   srcClient,
		destClient:  destClient,
	}
}

func (g *replicationGroup) runSync(ctx context.Context, threadCount int, hashStore *SimpleStore) {
	for i := 0; i < threadCount; i++ {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			for {
				select {
				case <-ctx.Done():
					klog.Info("exiting go routine as context canceled/timeout...")
					return
				case sd, ok := <-g.toReplicate:
					if !ok {
						klog.Info("exiting sync go routine as replicate channel closed")
						return
					}
					g.sync(ctx, sd, hashStore)
				}
			}
		}()
	}
}

func (g *replicationGroup) copy(ctx context.Context, sd srcDest) (sizeBytes int64, retErr error) {
	sr, err := g.srcClient.read(ctx, sd.src)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := sr.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()

	dw, err := g.destClient.write(ctx, sd.dest)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := dw.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()

	// TODO TeeReader can copy and calculate hash.
	size, err := io.Copy(dw, sr)
	if err != nil {
		return 0, err
	}

	return size, nil
}

func (g *replicationGroup) sync(ctx context.Context, sd srcDest, hashStore *SimpleStore) {
	klog.InfoS("syncing", "src", sd.src, "dest", sd.dest)

	changed, err := g.changed(ctx, sd.src, hashStore)
	// err in change detection will not be added to errCount
	if err == nil && !changed {
		klog.InfoS("skip sync as the src was replicated and mtime unchanged", "src", sd.src)
		return
	}

	// if change detection failed or change detected, continue copy
	start := time.Now()
	sizeBytes, err := g.copy(ctx, sd)
	if err != nil {
		atomic.AddUint64(&g.errCount, 1)
		klog.ErrorS(err, "failed to copy a file", "src", sd.src, "dest", sd.dest)
		return
	}
	end := time.Now()
	rate := float64(sizeBytes) / (end.Sub(start).Seconds())
	klog.InfoS("copy", "src", sd.src, "dest", sd.dest, "throughput", fmt.Sprintf("%f MB/s", rate/1024/1024))

	hash, err := g.validateHash(ctx, sd.src, sd.dest)
	if err != nil {
		atomic.AddUint64(&g.errCount, 1)
		klog.ErrorS(err, "failed to validate the hash of a file", "src", sd.src, "dest", sd.dest)
		return
	}

	t, err := g.srcClient.mtime(ctx, sd.src)
	if err != nil {
		atomic.AddUint64(&g.errCount, 1)
		klog.ErrorS(err, "failed to read mtime from a file", "src", sd.src)
	}

	hashStore.Lock()
	defer hashStore.UnLock()
	if err := hashStore.write(ctx, sd.src, LogHashEntry{
		Crc32cHash:  hash,
		ReplicaPath: sd.dest,
		ModTime:     t,
	}); err != nil {
		atomic.AddUint64(&g.errCount, 1)
		klog.ErrorS(err, "failed to store hash in metadata", "src", sd.src, "dest", sd.dest, "hash", hash)
	}
	klog.InfoS("syncing done", "src", sd.src, "dest", sd.dest)
}

func (g *replicationGroup) validateHash(ctx context.Context, src, dest string) (string, error) {
	srcHash, err := g.srcClient.hash(ctx, src)
	if err != nil {
		return "", err
	}
	destHash, err := g.destClient.hash(ctx, dest)
	if err != nil {
		return "", err
	}
	srcEncoded := base64.StdEncoding.EncodeToString(srcHash)
	destEncoded := base64.StdEncoding.EncodeToString(destHash)
	if srcEncoded != destEncoded {
		return "", fmt.Errorf("hash mismatched src %q=%s, dest %q=%s", src, srcHash, dest, destHash)
	}

	return destEncoded, nil
}

func (g *replicationGroup) changed(ctx context.Context, src string, hashStore *SimpleStore) (bool, error) {
	storedHash := LogHashEntry{}
	hashStore.Lock()
	err := hashStore.Read(ctx, src, &storedHash)
	hashStore.UnLock()
	// either file corrupted or not replicated
	if err != nil || storedHash.ReplicaPath == "" {
		return true, err
	}

	klog.InfoS("found existing hash", "src", src, "storedHash", storedHash)
	currentModTime, err := g.srcClient.mtime(ctx, src)
	if err != nil {
		return true, err
	}

	klog.InfoS("mod time",
		"src", src, "stored mtime", storedHash.ModTime, "current mtime", currentModTime)

	return !currentModTime.Equal(storedHash.ModTime), nil
}

func (g *replicationGroup) runCopy(ctx context.Context, threadCount int) {
	for i := 0; i < threadCount; i++ {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			for {
				select {
				case <-ctx.Done():
					klog.Info("exiting go routine as context canceled/timeout...")
					return
				case sd, ok := <-g.toReplicate:
					if !ok {
						klog.Info("exiting sync go routine as replicate channel closed")
						return
					}
					start := time.Now()
					sizeBytes, err := g.copy(ctx, sd)
					if err != nil {
						atomic.AddUint64(&g.errCount, 1)
						klog.ErrorS(err, "failed to copy a file", "src", sd.src, "dest", sd.dest)
						continue
					}
					end := time.Now()
					rate := float64(sizeBytes) / (end.Sub(start).Seconds())
					klog.InfoS("copy", "src", sd.src, "dest", sd.dest, "throughput", fmt.Sprintf("%f MB/s", rate/1024/1024))
				}
			}
		}()
	}
}

func (g *replicationGroup) wait() {
	g.wg.Wait()
}

func runReplication(ctx context.Context, srcDir, destDir string, localClient *fsClient, remoteClient storageClient, hashStore *SimpleStore) error {
	start := time.Now()
	defer func() {
		klog.InfoS("runReplication", "used time", time.Now().Sub(start))
	}()
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(syncTimeout))
	defer cancel()

	toReplicate := make(chan srcDest)
	group := newReplicationGroup(toReplicate, localClient, remoteClient)
	group.runSync(ctx, replicationThreadCount, hashStore)

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			klog.Error(err, "failed to access a path %q: %v", path, err)
			return err
		}

		info, err := d.Info()
		if err != nil {
			klog.Error(err, "failed to read a file info", "dest", d)
			return err
		}

		sub, err := filepath.Rel(srcDir, path)
		if err != nil {
			klog.Error(err, "failed to find a rel path", "srcDir", srcDir, "path", path)
			return err
		}

		dest := destDir + sub
		if !strings.HasSuffix(destDir, "/") {
			dest = destDir + "/" + sub
		}

		if d.IsDir() {
			if err := remoteClient.mkdirp(ctx, dest, info.Mode()); err != nil {
				klog.Error(err, "failed to create a dir", "dest", dest)
				return err
			}
			return nil
		}

		toReplicate <- srcDest{
			src:  path,
			dest: dest,
		}
		return nil
	})

	// stop group goroutines
	close(toReplicate)
	group.wait()

	if group.errCount > 0 || err != nil {
		return fmt.Errorf("replication completed with errors sync error count: %d, walk dir error: %v", group.errCount, err)
	}
	klog.Info("replication successfully completed")

	return nil
}

// LogMetadata stores metadata information for redo logs.
type LogMetadata struct {
	// KeyToLogEntry stores redo logs information in a map.
	// key is used for deduplicate
	KeyToLogEntry map[string]LogMetadataEntry
}

// LogMetadataEntry stores metadata information for a redo log.
type LogMetadataEntry struct {
	// LogHashEntry stores hash information for a redo log.
	LogHashEntry
	// SrcPath stores the path of a redo log in original environment.
	SrcPath string
	// FirstChange stores the first SCN(inclusive) of a redo log.
	FirstChange string // SCN inclusive
	// NextChange stores the next SCN(exclusive) of a redo log.
	NextChange string
	// 	FirstChange stores the first timestamp (inclusive) of a redo log.
	//	we used TO_CHAR(DATE, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	FirstTime time.Time
	// 	NextTime stores the next timestamp (inclusive) of a redo log.
	//	we used TO_CHAR(DATE, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	NextTime time.Time
	// CompletionTime stores the timestamp when a redo log was archived.
	CompletionTime string
	// Sequence stores the sequence of a archived redo log.
	Sequence string
	// Incarnation stores the Incarnation of a archived redo log.
	Incarnation string
	// Thread stores the redo thread number
	Thread string
}

// LogHashEntry stores hash information for a redo log.
type LogHashEntry struct {
	// Crc32cHash stores the crc32c hash of a redo log.
	Crc32cHash string
	// ReplicaPath stores the path of a replicated redo log.
	ReplicaPath string
	// ModTime stores the mod time of a redo log after replication.
	ModTime time.Time
}

// SimpleStore implements a simple data store to read and write golang objects.
type SimpleStore struct {
	mu sync.Mutex
	sync.RWMutex
	c       storageClient
	dataDir string
}

// NewSimpleStore returns a SimpleStore, SimpleStore will store data under the specified dataDir.
// '/' will be appended to the dataDir if it does not end with '/'
func NewSimpleStore(ctx context.Context, dataDir string) (*SimpleStore, error) {
	var c storageClient
	if strings.HasPrefix(dataDir, gsPrefix) {
		gc, err := newGcsClient(ctx)
		if err != nil {
			return nil, err
		}
		c = gc
	} else {
		c = &fsClient{}
	}
	if !strings.HasSuffix(dataDir, "/") {
		dataDir = dataDir + "/"
	}

	return &SimpleStore{
		mu:      sync.Mutex{},
		c:       c,
		dataDir: dataDir,
	}, nil
}

// Close closes the storage client of the store.
func (s *SimpleStore) Close(ctx context.Context) error {
	return s.c.close(ctx)
}

// Read retrieves a golang object from this SimpleStore and decode it into data.
// This method is unsafe for concurrent use. It's caller's responsibility to call
// SimpleStore.Lock()/SimpleStore.UnLock() for synchronization between goroutines.
func (s *SimpleStore) Read(ctx context.Context, path string, data interface{}) (retErr error) {
	if data == nil {
		return fmt.Errorf("invalid input %v", data)
	}
	value := reflect.ValueOf(data)
	if value.Type().Kind() != reflect.Ptr {
		return errors.New("attempt to store the retrieved data into a non-pointer")
	}
	dataPath := s.dataDir + path

	r, err := s.c.read(ctx, dataPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := r.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()

	dec := gob.NewDecoder(r)
	return dec.Decode(data)
}

// write writes a golang object into this SimpleStore.
// This method is unsafe for concurrent use. It's caller's responsibility to call
// SimpleStore.Lock()/SimpleStore.UnLock() for synchronization between goroutines.
func (s *SimpleStore) write(ctx context.Context, path string, data interface{}) (retErr error) {
	w, err := s.c.write(ctx, s.dataDir+path)
	if err != nil {
		return err
	}
	defer func() {
		if err := w.Close(); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; %v", retErr, err)
			}
		}
	}()
	enc := gob.NewEncoder(w)
	return enc.Encode(data)
}

// Lock locks this store.
func (s *SimpleStore) Lock() {
	s.mu.Lock()
}

// UnLock unlocks this store.
func (s *SimpleStore) UnLock() {
	s.mu.Unlock()
}

// delete deletes the named path and its associated golang object.
func (s *SimpleStore) delete(ctx context.Context, path string) (retErr error) {
	return s.c.delete(ctx, path, true)
}

type logSyncer struct {
	dest         string
	dbdClient    dbdpb.DatabaseDaemonClient
	localClient  *fsClient
	remoteClient storageClient
	hashStore    *SimpleStore
}

func (l *logSyncer) run(ctx context.Context) error {
	src, err := getArchivedLogDir(ctx, l.dbdClient)
	if err != nil {
		// cannot get log dir to start sync
		return err
	}
	err = runReplication(ctx, src, l.dest, l.localClient, l.remoteClient, l.hashStore)
	if err != nil {
		klog.ErrorS(err, "initial sync failed")
	}
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("Exiting syncer go routine as context canceled/timeout...")
			return nil

		case <-ticker.C:
			err = runReplication(ctx, src, l.dest, l.localClient, l.remoteClient, l.hashStore)
			if err != nil {
				klog.ErrorS(err, "sync failed")
			} else {
				// check whether log location changed or not
				if newSrc, err := getArchivedLogDir(ctx, l.dbdClient); err != nil {
					src = newSrc
				}
			}
		}
	}
}

func getArchivedLogDir(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient) (string, error) {
	resp, err := dbdClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{"select name from v$recovery_file_dest"}, Suppress: false})
	if err != nil {
		return "", err
	}

	if len(resp.GetMsg()) <= 0 {
		return "", fmt.Errorf("getArchivedLogDir: failed to find the recovery_file_dest from %v", resp)
	}

	row := make(map[string]string)
	if err := json.Unmarshal([]byte(resp.GetMsg()[0]), &row); err != nil {
		return "", err
	}
	return row["NAME"], nil
}

// RunLogReplication starts redo logs replication.
// It runs below steps repeatedly
// Read archived redo logs location with dbdClient at very beginning or after a success sync.,
// sync redo logs to dest specified location.
func RunLogReplication(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient, dest string, hashStore *SimpleStore) error {
	local := &fsClient{}
	var remote storageClient
	if strings.HasPrefix(dest, gsPrefix) {
		c, err := newGcsClient(ctx)
		if err != nil {
			return err
		}
		remote = c
	} else {
		remote = local
	}
	defer func() {
		local.close(ctx)
		remote.close(ctx)
	}()
	syncer := &logSyncer{
		dest:         dest,
		dbdClient:    dbdClient,
		localClient:  local,
		remoteClient: remote,
		hashStore:    hashStore,
	}
	return syncer.run(ctx)
}

// SetArchiveLag sets archive lag target parameter in the database if the value is 0.
func SetArchiveLag(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient) error {
	resp, err := dbdClient.RunSQLPlusFormatted(
		ctx,
		&dbdpb.RunSQLPlusCMDRequest{Commands: []string{fmt.Sprintf("select value from v$parameter where name='%s'", archiveLagParam)}, Suppress: false},
	)
	if err != nil {
		return fmt.Errorf("SetArchiveLag: failed to get archive lag value : %v", err)
	}

	lag := "0"
	if len(resp.GetMsg()) > 0 {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(resp.GetMsg()[0]), &row); err != nil {
			return fmt.Errorf("SetArchiveLag: failed to parse %v", resp)
		}
		lag = row["VALUE"]
	}

	if lag != "0" {
		klog.Info("SetArchiveLag: found archive lag parameter set, skip update. ", "value=", lag)
		return nil
	}

	cmd := fmt.Sprintf("alter system set %s=%d scope=both", archiveLagParam, archiveLagVal)
	_, err = dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{cmd},
		Suppress: false,
	})
	if err != nil {
		return fmt.Errorf("SetArchiveLag: failed to exectue parameter command: %q", cmd)
	}
	return nil
}

// RunMetadataUpdate starts metadata update.
// It runs below steps repeatedly
// Read archived redo logs view with dbdClient, cumulatively update log metadata into metaStore.
func RunMetadataUpdate(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient, hashStore *SimpleStore, metadataStore *SimpleStore) error {
	if err := metadataUpdate(ctx, dbdClient, hashStore, metadataStore); err != nil {
		klog.ErrorS(err, "failed to update log metadata")
	}
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("Exiting log metadata update go routine as context canceled/timeout...")
			return nil

		case <-ticker.C:
			if err := metadataUpdate(ctx, dbdClient, hashStore, metadataStore); err != nil {
				klog.ErrorS(err, "failed to update log metadata")
			}
		}
	}
}

func metadataUpdate(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient, hashStore *SimpleStore, metadataStore *SimpleStore) error {
	archiveDir, err := getArchivedLogDir(ctx, dbdClient)
	if err != nil {
		return err
	}
	// TODO based on retention/ keep track the last success status update timestamp,
	// we can select log COMPLETION_TIME >= NOW - RETENTION to reduce the size of result.
	// Assume El Carro instance date is in UTC
	query := "select " +
		"v$archived_log.NAME, " +
		"v$archived_log.FIRST_CHANGE#, " +
		"TO_CHAR(v$archived_log.FIRST_TIME, 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') as FIRST_TIME, " +
		"v$archived_log.NEXT_CHANGE#, " +
		"TO_CHAR(v$archived_log.NEXT_TIME, 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') as NEXT_TIME, " +
		"v$archived_log.SEQUENCE#, " +
		"TO_CHAR(v$archived_log.COMPLETION_TIME, 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z00:00\"') as COMPLETION_TIME, " +
		"v$database_incarnation.INCARNATION#, " +
		"v$archived_log.THREAD# " +
		"from v$archived_log left join v$database_incarnation on v$archived_log.RESETLOGS_ID=v$database_incarnation.RESETLOGS_ID " +
		"where v$archived_log.COMPLETION_TIME >= (SYSDATE - 30) AND  v$archived_log.NAME LIKE '" + archiveDir + "%'"
	resp, err := dbdClient.RunSQLPlusFormatted(ctx,
		&dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{query},
			Suppress: true,
		},
	)

	if err != nil {
		return err
	}

	// read and update metadata catalog
	metadataStore.Lock()
	defer metadataStore.UnLock()
	metadata := &LogMetadata{}

	// TODO better retry or implement retry in store.
	for i := 0; i <= 5; i++ {
		if err := metadataStore.Read(ctx, MetadataStorePath, metadata); err != nil {
			klog.ErrorS(err, "failed to load metadata", "attempt", i)
		} else {
			break
		}
	}

	keys := []string{"INCARNATION#", "SEQUENCE#", "NAME", "FIRST_TIME", "NEXT_TIME", "FIRST_CHANGE#", "NEXT_CHANGE#", "COMPLETION_TIME", "THREAD#"}
	for _, msg := range resp.GetMsg() {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(msg), &row); err == nil {
			vals := make([]string, len(keys))
			for i, key := range keys {
				v, ok := row[key]
				if !ok {
					klog.Errorf("cannot find %s from view %+v", key, row)
				}
				vals[i] = v
			}
			startTime, err := time.Parse(time.RFC3339, vals[3])
			if err != nil {
				klog.Error(err, "failed to parse the start time")
				continue
			}
			nextTime, err := time.Parse(time.RFC3339, vals[4])
			if err != nil {
				klog.Error(err, "failed to parse the end time")
				continue
			}

			if metadata.KeyToLogEntry == nil {
				metadata.KeyToLogEntry = make(map[string]LogMetadataEntry)
			}

			key := fmt.Sprintf("%s-%s-%s", vals[8], vals[0], vals[1])

			if existingEntry, ok := metadata.KeyToLogEntry[key]; ok {
				if existingEntry.ReplicaPath != "" {
					// already included in metadata
					continue
				}
			}

			// vals "INCARNATION#", "SEQUENCE#", "NAME", "FIRST_TIME", "NEXT_TIME", "FIRST_CHANGE#", "NEXT_CHANGE#", "COMPLETION_TIME", "THREAD#"
			log := LogMetadataEntry{
				Incarnation:    vals[0],
				Sequence:       vals[1],
				SrcPath:        vals[2],
				FirstTime:      startTime,
				NextTime:       nextTime,
				FirstChange:    vals[5],
				NextChange:     vals[6],
				CompletionTime: vals[7],
				Thread:         vals[8],
			}

			hashEntry := LogHashEntry{}
			hashStore.Lock()
			if err := hashStore.Read(ctx, vals[2], &hashEntry); err == nil {
				log.LogHashEntry = hashEntry
			}
			hashStore.UnLock()
			metadata.KeyToLogEntry[key] = log
		}
	}
	return metadataStore.write(ctx, MetadataStorePath, metadata)
}

// RunLogRetention starts redo logs cleanup.
func RunLogRetention(ctx context.Context, retentionDays int, metadataStore *SimpleStore, hashStore *SimpleStore) error {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("Exiting log retention go routine as context canceled/timeout...")
			return nil

		case <-ticker.C:
			c, err := newGcsClient(ctx)
			if err != nil {
				klog.ErrorS(err, "failed to create a GCS client")
				continue
			}
			if err := cleanUpLogs(ctx, c, retentionDays, metadataStore, hashStore); err != nil {
				klog.ErrorS(err, "failed to clean up expired logs")
			}
			if err := c.close(ctx); err != nil {
				klog.ErrorS(err, "failed to close a GCS client")
			}
		}
	}
}

func cleanUpLogs(ctx context.Context, c storageClient, retentionDays int, metadataStore *SimpleStore, hashStore *SimpleStore) error {
	// read and update metadata catalog
	metadataStore.Lock()
	defer metadataStore.UnLock()
	metadata := &LogMetadata{}
	if err := metadataStore.Read(ctx, MetadataStorePath, metadata); err != nil {
		return err
	}
	newMetadata := &LogMetadata{}
	newMetadata.KeyToLogEntry = make(map[string]LogMetadataEntry)

	e := timeNow().AddDate(0, 0, -retentionDays)
	// round expire time
	// For a timestamp day=X hour=[0 - 24), code clean up logs before day=(X-retention)
	// expire time is day=(X-retention) hour=0
	expire := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, e.Location())
	klog.Info(fmt.Sprintf("Cleaning up logs, Logs with next time before expiration time %v will be deleted.", expire))
	for k, v := range metadata.KeyToLogEntry {
		deleted := false
		if expire.After(v.NextTime) {
			if err := deleteLog(ctx, v, expire, c, hashStore); err != nil {
				klog.Error(err, "failed to delete the expired log")
			} else {
				deleted = true
			}
		}
		if !deleted {
			newMetadata.KeyToLogEntry[k] = v
		}
	}

	return metadataStore.write(ctx, MetadataStorePath, newMetadata)
}

func deleteLog(ctx context.Context, entry LogMetadataEntry, expire time.Time, c storageClient, hashStore *SimpleStore) error {
	// Precautious to not delete unexpected logs, double check date in the dir.
	// gs://test/mydb/MYDB_USCENTRAL1A/archivelog/2021_09_13/o1_mf_1_1_jmz1gbon_.arc returns 2021_09_13.
	dateDir := filepath.Base(filepath.Dir(entry.ReplicaPath))
	layout := "2006_01_02"
	tFromDir, err := time.Parse(layout, dateDir)
	if err != nil {
		return fmt.Errorf("failed to parse time from dir %v: %v", dateDir, err)
	}
	// only delete logs before expire date
	if !tFromDir.Before(expire) {
		return fmt.Errorf("skip deletion, date in the path(%q) not expired", entry.ReplicaPath)
	}
	klog.Info(fmt.Sprintf("Cleaning up an expired log %+v", entry))
	if err := c.delete(ctx, entry.ReplicaPath, true); err != nil {
		return fmt.Errorf("failed to delete %v: %v", entry.ReplicaPath, err)
	}
	hashStore.Lock()
	defer hashStore.UnLock()
	return hashStore.delete(ctx, entry.SrcPath)
}

// Merge reads all metadata and merge time ranges covered by replicated redo logs.
func Merge(metadata LogMetadata) [][]string {
	if len(metadata.KeyToLogEntry) == 0 {
		return nil
	}

	keys := make([]string, len(metadata.KeyToLogEntry))
	i := 0
	for k := range metadata.KeyToLogEntry {
		keys[i] = k
		i += 1
	}
	sort.Slice(keys, func(i, j int) bool {
		return metadata.KeyToLogEntry[keys[i]].FirstTime.Before(metadata.KeyToLogEntry[keys[j]].FirstTime)
	})

	kToEntry := metadata.KeyToLogEntry

	var windows [][]string
	var currStartKey string
	var currEndKey string

	for _, k := range keys {
		if kToEntry[k].ReplicaPath == "" {
			// not replicated
			continue
		}
		if currStartKey == "" {
			// start a new range
			currStartKey = k
			currEndKey = k
			continue
		}

		// TODO can we assume the end time of previous log must equal to the first time of next log ?
		if kToEntry[currEndKey].NextTime.Equal(kToEntry[k].FirstTime) {
			// merge the range
			currEndKey = k
		} else {
			windows = append(windows, []string{currStartKey, currEndKey})
			currStartKey = k
			currEndKey = k
		}
	}

	if currStartKey != "" {
		windows = append(windows, []string{currStartKey, currEndKey})
	}
	return windows
}

// StageLogs copies redo logs from src dir to dest dir.
func StageLogs(ctx context.Context, destDir string, include func(entry LogMetadataEntry) bool, logPath string) error {
	metadataStore, err := NewSimpleStore(ctx, logPath)
	if err != nil {
		return fmt.Errorf("failed to create a metadata store %v", err)
	}
	metadata := LogMetadata{}
	if err := metadataStore.Read(ctx, MetadataStorePath, &metadata); err != nil {
		return fmt.Errorf("failed to read metadata: %v", err)
	}

	n := len(metadata.KeyToLogEntry)
	if n == 0 {
		return fmt.Errorf("empty metadata: %v", metadata)
	}

	var toStage []LogMetadataEntry
	var notReplicated []LogMetadataEntry

	for _, v := range metadata.KeyToLogEntry {
		if include(v) {
			if v.ReplicaPath != "" {
				toStage = append(toStage, v)
			} else {
				notReplicated = append(notReplicated, v)
			}
		}
	}

	if len(notReplicated) > 0 {
		return fmt.Errorf("cannot find redo logs in replica location %+v", notReplicated)
	}

	if len(toStage) == 0 {
		klog.InfoS("no logs need to be staged")
		return nil
	}

	destClient := &fsClient{}
	var srcClient storageClient
	if strings.HasPrefix(toStage[0].ReplicaPath, gsPrefix) {
		c, err := newGcsClient(ctx)
		if err != nil {
			return err
		}
		srcClient = c
	} else {
		srcClient = &fsClient{}
	}
	defer func() {
		srcClient.close(ctx)
		destClient.close(ctx)
	}()
	if err := srcClient.mkdirp(ctx, destDir, 0750); err != nil {
		return fmt.Errorf("failed to create the stage dir: %v", err)
	}
	toReplicate := make(chan srcDest)
	group := newReplicationGroup(toReplicate, srcClient, destClient)
	group.runCopy(ctx, replicationThreadCount)
	for _, ts := range toStage {
		toReplicate <- srcDest{
			src:  ts.ReplicaPath,
			dest: filepath.Join(destDir, filepath.Base(ts.SrcPath)),
		}
	}
	// stop group goroutines
	close(toReplicate)
	group.wait()

	if group.errCount > 0 {
		return fmt.Errorf("stage completed with errors error count: %d", group.errCount)
	}
	klog.Info("stage successfully completed")

	return nil
}
