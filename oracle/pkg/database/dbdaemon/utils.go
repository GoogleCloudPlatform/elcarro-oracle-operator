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
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"k8s.io/klog/v2"
)

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
	datapatch = func(databaseHome string) string {
		return filepath.Join(databaseHome, "OPatch", "datapatch")
	}
)

const (
	contentTypePlainText = "plain/text"
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
	sanitizedParams := params
	switch bin {
	case rman(ohome), impdp(ohome), expdp(ohome):
		sanitizedParams = append([]string{"***"}, params[1:]...)
	case lsnrctl(ohome), orapwd(ohome), datapatch(ohome):
	default:
		klog.InfoS("command not supported", "bin", bin)
		return fmt.Errorf("command %q is not supported", bin)
	}
	klog.InfoS("executing command with args", "cmd", bin, "params", sanitizedParams, "ORACLE_SID", os.Getenv("ORACLE_SID"), "ORACLE_HOME", ohome, "TNS_ADMIN", os.Getenv("TNS_ADMIN"))
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
	w := bufio.NewWriterSize(f, 16*1024*1024)
	defer func() {
		if err := w.Flush(); err != nil {
			klog.Warningf("failed to flush %v: %v", w, err)
		}
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()
	if _, err := io.Copy(w, content); err != nil {
		return fmt.Errorf("copying contents failed: %v", err)
	}
	return nil
}

func (o *osUtilImpl) removeFile(file string) error {
	return os.Remove(file)
}
