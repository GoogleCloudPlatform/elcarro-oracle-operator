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

package controllers

import (
	"bytes"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
	log "k8s.io/klog/v2"
)

// ExecCmdParams stores parameters for invoking pod/exec.
type ExecCmdParams struct {
	Pod        string
	Ns         string
	Con        *corev1.Container
	Sch        *runtime.Scheme
	RestConfig *rest.Config
	Client     kubernetes.Interface
}

// ExecCmdFunc  invokes pod/exec.
var ExecCmdFunc = func(p ExecCmdParams, cmd string) (string, error) {
	var cmdOut, cmdErr bytes.Buffer

	cmdShell := []string{"sh", "-c", cmd}

	req := p.Client.CoreV1().RESTClient().Post().Resource("pods").Name(p.Pod).
		Namespace(p.Ns).SubResource("exec")

	req.VersionedParams(&corev1.PodExecOptions{
		Container: p.Con.Name,
		Command:   cmdShell,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(p.RestConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to init executor: %v", err)
	}

	// exec.Stream might return timout error, use a backoff with 4 retries
	// 100ms, 500ms, 2.5s, 12.5s
	var backoff = wait.Backoff{
		Steps:    4,
		Duration: 100 * time.Millisecond,
		Factor:   5.0,
		Jitter:   0.1,
	}
	if err := retry.OnError(backoff, func(error) bool { return true }, func() error {
		e := exec.Stream(remotecommand.StreamOptions{
			Stdout: &cmdOut,
			Stderr: &cmdErr,
			Tty:    false,
		})
		if e != nil {
			log.Error(fmt.Sprintf("exec.Stream failed, retrying, err: %v, stderr: %v, stdout: %v",
				err, cmdErr.String(), cmdOut.String()))
		}
		return e
	}); err != nil {
		return "", fmt.Errorf("failed to run a command [%v], err: %v, stderr: %v, stdout: %v",
			cmd, err, cmdErr.String(), cmdOut.String())
	}

	if cmdErr.Len() > 0 {
		return "", fmt.Errorf("stderr: %v", cmdErr.String())
	}

	return cmdOut.String(), nil
}
