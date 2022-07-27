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

package finalizer

import "sigs.k8s.io/controller-runtime/pkg/client"

// Exists returns whether an object has a specific finalizer.
func Exists(obj client.Object, finalizer string) bool {
	for _, s := range obj.GetFinalizers() {
		if s == finalizer {
			return true
		}
	}
	return false
}

// Remove removes a specific finalizer from an object.
func Remove(obj client.Object, finalizer string) {
	var filtered []string
	for _, s := range obj.GetFinalizers() {
		if s != finalizer {
			filtered = append(filtered, s)
		}
	}
	obj.SetFinalizers(filtered)
}
