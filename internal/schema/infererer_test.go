// Copyright 2023 EMQ Technologies Co., Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferFromSchemaFileError(t *testing.T) {
	tests := []struct {
		name       string
		schemaType string
		schemaId   string
		err        error
	}{
		{
			name:       "test weak schema type",
			schemaType: "can",
			schemaId:   "aa",
			err:        nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InferFromSchemaFile(tt.schemaType, tt.schemaId)
			assert.Equal(t, tt.err, err)
		})
	}
}
