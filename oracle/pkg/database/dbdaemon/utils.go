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

package dbdaemon

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const gsPrefix = "gs://"

// Override library functions for the benefit of unit tests.
var (
	lsnrctl = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "lsnrctl")
	}
	rman = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "rman")
	}
	dgmgrl = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "dgmgrl")
	}
	tnsping = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "tnsping")
	}
	orapwd = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "orapwd")
	}
	impdp = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "impdp")
	}
	expdp = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "expdp")
	}
)

const (
	contentTypePlainText = "plain/text"
	contentTypeGZ        = "application/gzip"
)

// osUtil was defined for tests.
type osUtil interface {
	runCommand(bin string, params []string) error
	isReturnCodeEqual(err error, code int) bool
	createFile(file string, content io.Reader) error
	removeFile(file string) error
}

type osUtilImpl struct {
}

func (o *osUtilImpl) runCommand(bin string, params []string) error {
	ohome := os.Getenv("ORACLE_HOME")
	klog.InfoS("executing command with args", "cmd", bin, "params", params, "ORACLE_SID", os.Getenv("ORACLE_SID"), "ORACLE_HOME", ohome, "TNS_ADMIN", os.Getenv("TNS_ADMIN"))
	switch bin {
	case lsnrctl(ohome), rman(ohome), orapwd(ohome), impdp(ohome), expdp(ohome):
	default:
		klog.InfoS("command not supported", "bin", bin)
		return fmt.Errorf("command %q is not supported", bin)
	}
	cmd := exec.Command(bin)
	cmd.Args = append(cmd.Args, params...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (o *osUtilImpl) isReturnCodeEqual(err error, code int) bool {
	if exitError, ok := err.(*exec.ExitError); ok {
		return exitError.ExitCode() == code
	}
	return false
}

func (o *osUtilImpl) createFile(file string, content io.Reader) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("couldn't create dir err: %v", err)
	}
	f, err := os.Create(file) // truncates if file exists.
	if err != nil {
		return fmt.Errorf("couldn't create file err: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()
	if _, err := io.Copy(f, content); err != nil {
		return fmt.Errorf("copying contents failed: %v", err)
	}
	return nil
}

func (o *osUtilImpl) removeFile(file string) error {
	return os.Remove(file)
}

// GCSUtil contains helper methods for reading/writing GCS objects.
type GCSUtil interface {
	// Download returns an io.ReadCloser for GCS object at given gcsPath.
	Download(ctx context.Context, gcsPath string) (io.ReadCloser, error)
	// UploadFile uploads contents of a file at filepath to gcsPath location in
	// GCS and sets object's contentType.
	// If gcsPath ends with .gz it also compresses the uploaded contents
	// and sets object's content type to application/gzip.
	UploadFile(ctx context.Context, gcsPath, filepath, contentType string) error
	// SplitURI takes a GCS URI and splits it into bucket and object names. If the URI does not have
	// the gs:// scheme, or the URI doesn't specify both a bucket and an object name, returns an error.
	SplitURI(url string) (bucket, name string, err error)
}

type gcsUtilImpl struct{}

func (g *gcsUtilImpl) Download(ctx context.Context, gcsPath string) (io.ReadCloser, error) {
	bucket, name, err := g.SplitURI(gcsPath)
	if err != nil {
		return nil, err
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to init GCS client: %v", err)
	}
	defer client.Close()

	reader, err := client.Bucket(bucket).Object(name).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read URL %s: %v", gcsPath, err)
	}

	return reader, nil
}

func (g *gcsUtilImpl) UploadFile(ctx context.Context, gcsPath, filePath, contentType string) error {
	return retry.OnError(retry.DefaultBackoff, func(err error) bool {
		klog.ErrorS(err, "failed to upload a file")
		// tried to cast err to *googleapi.Error with errors.As and wrap the error
		// in uploadFile. returned err is not a *googleapi.Error.
		return err != nil && strings.Contains(err.Error(), "compute: Received 500 ")
	}, func() error {
		return g.uploadFile(ctx, gcsPath, filePath, contentType)
	})

}

// uploadFile is the implementation of UploadFile to be wrapped with retry logic.
func (g *gcsUtilImpl) uploadFile(ctx context.Context, gcsPath, filePath, contentType string) error {
	bucket, name, err := g.SplitURI(gcsPath)
	if err != nil {
		return err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()

	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to init GCS client: %v", err)
	}
	defer client.Close()

	b := client.Bucket(bucket)
	// check if bucket exists and it is accessible
	/* YOLO
	if _, err := b.Attrs(ctx); err != nil {
		return err
	}
	*/

	gcsWriter := b.Object(name).NewWriter(ctx)
	gcsWriter.ContentType = contentType

	var writer io.WriteCloser = gcsWriter
	if strings.HasSuffix(gcsPath, ".gz") {
		gcsWriter.ContentType = contentTypeGZ
		writer = gzip.NewWriter(gcsWriter)
	}

	_, err = io.Copy(writer, f)
	if err != nil {
		return fmt.Errorf("failed to write file %s to %s: %v", filePath, gcsPath, err)
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("failed to complete writing file %s to %s: %v", filePath, gcsPath, err)
	}
	if err = gcsWriter.Close(); err != nil {
		return fmt.Errorf("failed to complete writing file %s to %s: %v", filePath, gcsPath, err)
	}

	return nil
}

func (g *gcsUtilImpl) SplitURI(url string) (bucket, name string, err error) {
	u := strings.TrimPrefix(url, gsPrefix)
	if u == url {
		return "", "", fmt.Errorf("URL %q is missing the %q prefix", url, gsPrefix)
	}
	if i := strings.Index(u, "/"); i >= 2 {
		return u[:i], u[i+1:], nil
	}
	return "", "", fmt.Errorf("URL %q does not specify a bucket and a name", url)
}
