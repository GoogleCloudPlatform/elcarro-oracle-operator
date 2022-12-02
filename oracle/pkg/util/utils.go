// Copyright 2022 Google LLC
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

package util

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const (
	GSPrefix      = "gs://"
	contentTypeGZ = "application/gzip"
)

// GCSUtil contains helper methods for reading/writing GCS objects.
type GCSUtil interface {
	// Download returns an io.ReadCloser for GCS object at given gcsPath.
	Download(ctx context.Context, gcsPath string) (io.ReadCloser, error)
	// Delete deletes all objects under given gcsPath
	Delete(ctx context.Context, gcsPath string) error
	// UploadFile uploads contents of a file at filepath to gcsPath location in
	// GCS and sets object's contentType.
	// If gcsPath ends with .gz it also compresses the uploaded contents
	// and sets object's content type to application/gzip.
	UploadFile(ctx context.Context, gcsPath, filepath, contentType string) error
	// SplitURI takes a GCS URI and splits it into bucket and object names. If the URI does not have
	// the gs:// scheme, or the URI doesn't specify both a bucket and an object name, returns an error.
	SplitURI(url string) (bucket, name string, err error)
}

type GCSUtilImpl struct{}

func (g *GCSUtilImpl) Download(ctx context.Context, gcsPath string) (io.ReadCloser, error) {
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

func (g *GCSUtilImpl) UploadFile(ctx context.Context, gcsPath, filePath, contentType string) error {
	return retry.OnError(retry.DefaultBackoff, func(err error) bool {
		klog.ErrorS(err, "failed to upload a file")
		// tried to cast err to *googleapi.Error with errors.As and wrap the error
		// in uploadFile. returned err is not a *googleapi.Error.
		return err != nil && strings.Contains(err.Error(), "compute: Received 500 ")
	}, func() error {
		return g.uploadFile(ctx, gcsPath, filePath, contentType)
	})
}

func (g *GCSUtilImpl) uploadFile(ctx context.Context, gcsPath, filePath, contentType string) error {
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
	if _, err := b.Attrs(ctx); err != nil {
		return err
	}

	gcsWriter := b.Object(name).NewWriter(ctx)
	gcsWriter.ContentType = contentType
	defer gcsWriter.Close()

	var writer io.WriteCloser = gcsWriter
	if strings.HasSuffix(gcsPath, ".gz") {
		gcsWriter.ContentType = contentTypeGZ
		writer = gzip.NewWriter(gcsWriter)
		defer writer.Close()
	}

	_, err = io.Copy(writer, f)
	if err != nil {
		return fmt.Errorf("failed to write file %s to %s: %v", filePath, gcsPath, err)
	}

	return nil
}

func (g *GCSUtilImpl) SplitURI(url string) (bucket, name string, err error) {
	u := strings.TrimPrefix(url, GSPrefix)
	if u == url {
		return "", "", fmt.Errorf("URL %q is missing the %q prefix", url, GSPrefix)
	}
	if i := strings.Index(u, "/"); i >= 2 {
		return u[:i], u[i+1:], nil
	}
	return "", "", fmt.Errorf("URL %q does not specify a bucket and a name", url)
}

func (g *GCSUtilImpl) Delete(ctx context.Context, gcsPath string) error {
	bucket, prefix, err := g.SplitURI(gcsPath)
	if err != nil {
		return err
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to init GCS client: %v", err)
	}
	defer client.Close()

	it := client.Bucket(bucket).Objects(ctx, &storage.Query{
		Prefix: prefix,
	})
	for {
		objAttrs, err := it.Next()
		if err != nil && err != iterator.Done {
			return fmt.Errorf("Bucket(%q).Objects(): %v", bucket, err)
		}
		if err == iterator.Done {
			break
		}
		if err := client.Bucket(bucket).Object(objAttrs.Name).Delete(ctx); err != nil {
			return fmt.Errorf("failed to Delete object(%s): %v", objAttrs.Name, err)
		}
	}
	return nil
}

// Contains check whether given "elem" presents in "array"
func Contains(array []string, elem string) bool {
	for _, v := range array {
		if v == elem {
			return true
		}
	}
	return false
}

// Filter Returns a slice that doesn't contain element
func Filter(slice []string, element string) []string {
	//This implementation isn't the fastest, but it protects against slices containing a single element.
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != element {
			result = append(result, s)
		}
	}
	return result
}
