// Copyright 2022-2025 EMQ Technologies Co., Ltd.
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

package infra

import (
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/lf-edge/ekuiper/contract/v2/api"

	"github.com/lf-edge/ekuiper/v2/internal/conf"
)

// SafeRun will catch and return the panic error together with other errors
// When running in a rule, the whole rule must be in this mode
// The sub processes or go routines under a rule should also use this mode
// To make sure all rule panic won't affect the whole system
// Also consider running in this mode if the function should not affect the whole system
func SafeRun(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			conf.Log.Errorf("panic stack:%s", string(debug.Stack()))
			debug.PrintStack()
			switch x := r.(type) {
			case string:
				err = errors.New(x)
			case error:
				err = x
			default:
				err = fmt.Errorf("%#v", x)
			}
		}
	}()
	err = fn()
	return err
}

// DrainError a non-block function to send out the error to the error channel
// Only the first error will be sent out and received then the rule will be terminated
// Thus the latter error will just skip
// It is usually the error outlet of an op/rule.
func DrainError(ctx api.StreamContext, err error, errCh chan<- error) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1) // 1 means the caller of DrainError
		if ctx != nil {
			ctx.GetLogger().Errorf("runtime error from %s/l%d: %v", file, line, err)
		} else {
			conf.Log.Errorf("runtime error %s/l%d: %v", file, line, err)
		}
	}
	select {
	case errCh <- err:
	default:
	}
}

func MsgWithStack(msg string) string {
	const depth = 32
	var stackBuilder strings.Builder
	pcs := make([]uintptr, depth)
	n := runtime.Callers(2, pcs) // Skip 2 frames
	frames := runtime.CallersFrames(pcs[:n])

	for {
		frame, more := frames.Next()
		if !more {
			break
		}
		stackBuilder.WriteString(fmt.Sprintf("%s:%d %s\n", frame.File, frame.Line, frame.Function))
	}

	return fmt.Sprintf("%s\nStack:\n%s", msg, stackBuilder.String())
}
