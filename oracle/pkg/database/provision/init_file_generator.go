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

package provision

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
)

type initFileInput struct {
	SourceDBName string
	DestDBName   string
}

// LoadInitOraTemplate generates an init ora content using the template and the required parameters.
func (i *initFileInput) LoadInitOraTemplate(dbVersion string) (string, error) {
	templateName := InitOraTemplateName
	if dbVersion == consts.Oracle18c {
		templateName = InitOraXeTemplateName
	}
	t, err := template.New(filepath.Base(templateName)).ParseFiles(templateName)
	if err != nil {
		return "", fmt.Errorf("LoadInitOraTemplate: parsing %q failed: %v", templateName, err)
	}

	initOraBuf := &bytes.Buffer{}
	if err := t.Execute(initOraBuf, i); err != nil {
		return "", fmt.Errorf("LoadInitOraTemplate: executing %q failed: %v", templateName, err)
	}
	return initOraBuf.String(), nil
}
