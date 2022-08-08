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

package standbyhelpers

import (
	"encoding/json"
)

type StandbySettingErr struct {
	Type   StandbySettingErrType
	Detail string
}

type StandbySettingErrType int32

const (
	StandbySettingErr_UNKNOWN StandbySettingErrType = 0
	// Standby settings has issue that we can't continue.
	StandbySettingErr_CONNECTION_FAILURE            StandbySettingErrType = 1
	StandbySettingErr_INCOMPATIBLE_DATABASE_VERSION StandbySettingErrType = 2
	StandbySettingErr_INSUFFICIENT_PRIVILEGE        StandbySettingErrType = 3
	StandbySettingErr_INVALID_LOGGING_SETUP         StandbySettingErrType = 4
	StandbySettingErr_INVALID_DB_PARAM              StandbySettingErrType = 5
	StandbySettingErr_INVALID_SERVICE_IMAGE         StandbySettingErrType = 6
	StandbySettingErr_INVALID_CDB_NAME              StandbySettingErrType = 7
	StandbySettingErr_INTERNAL_ERROR                StandbySettingErrType = 8
)

func (x *StandbySettingErrType) String() string {
	bytes, _ := json.Marshal(*x)
	return string(bytes)
}
