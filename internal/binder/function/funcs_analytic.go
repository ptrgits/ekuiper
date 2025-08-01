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

package function

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/lf-edge/ekuiper/contract/v2/api"

	"github.com/lf-edge/ekuiper/v2/pkg/ast"
	"github.com/lf-edge/ekuiper/v2/pkg/cast"
)

// registerAnalyticFunc registers the analytic functions
// The last parameter of the function is always the partition key
func registerAnalyticFunc() {
	builtins["changed_col"] = builtinFunc{
		fType: ast.FuncTypeScalar,
		exec: func(ctx api.FunctionContext, args []interface{}) (interface{}, bool) {
			ignoreNull, ok := args[0].(bool)
			if !ok {
				return fmt.Errorf("first arg is not a bool but got %v", args[0]), false
			}
			if ignoreNull && args[1] == nil {
				return nil, true
			}
			validData, ok := args[len(args)-2].(bool)
			if !ok {
				return fmt.Errorf("when arg is not a bool but got %v", args[len(args)-2]), false
			}
			if !validData {
				return nil, true
			}
			key := args[len(args)-1].(string)
			lv, err := ctx.GetState(key)
			if err != nil {
				return err, false
			}
			if !reflect.DeepEqual(args[1], lv) {
				err := ctx.PutState(key, args[1])
				if err != nil {
					return err, false
				}
				return args[1], true
			}
			return nil, true
		},
		val: func(_ api.FunctionContext, args []ast.Expr) error {
			if err := ValidateLen(2, len(args)); err != nil {
				return err
			}
			if ast.IsNumericArg(args[0]) || ast.IsTimeArg(args[0]) || ast.IsStringArg(args[0]) {
				return ProduceErrInfo(0, "boolean")
			}
			return nil
		},
	}
	builtins["had_changed"] = builtinFunc{
		fType: ast.FuncTypeScalar,
		exec: func(ctx api.FunctionContext, args []interface{}) (interface{}, bool) {
			l := len(args) - 2
			if l <= 1 {
				return fmt.Errorf("expect more than one arg but got %d", len(args)), false
			}
			validData, ok := args[len(args)-2].(bool)
			if !ok {
				return fmt.Errorf("when arg is not a bool but got %v", args[len(args)-2]), false
			}
			if !validData {
				return false, true
			}
			ignoreNull, ok := args[0].(bool)
			if !ok {
				return fmt.Errorf("first arg is not a bool but got %v", args[0]), false
			}
			key := args[len(args)-1].(string)
			paraLen := len(args) - 2
			result := false
			for i := 1; i < paraLen; i++ {
				v := args[i]
				k := key + strconv.Itoa(i)
				if ignoreNull && v == nil {
					continue
				}
				lv, err := ctx.GetState(k)
				if err != nil {
					return fmt.Errorf("error getting state for %s: %v", k, err), false
				}
				if !reflect.DeepEqual(v, lv) {
					result = true
					err := ctx.PutState(k, v)
					if err != nil {
						return fmt.Errorf("error setting state for %s: %v", k, err), false
					}
				}
			}
			return result, true
		},
		val: func(_ api.FunctionContext, args []ast.Expr) error {
			if len(args) <= 1 {
				return fmt.Errorf("expect more than one arg but got %d", len(args))
			}
			if ast.IsNumericArg(args[0]) || ast.IsTimeArg(args[0]) || ast.IsStringArg(args[0]) {
				return ProduceErrInfo(0, "bool")
			}
			return nil
		},
	}

	builtins["lag"] = builtinFunc{
		fType: ast.FuncTypeScalar,
		exec: func(ctx api.FunctionContext, args []interface{}) (interface{}, bool) {
			l := len(args) - 2
			if l < 1 || l > 4 {
				return fmt.Errorf("expect from 1 to 4 args but got %d", l), false
			}
			key := args[len(args)-1].(string)
			v, err := ctx.GetState(key)
			if err != nil {
				return fmt.Errorf("error getting state for %s: %v", key, err), false
			}
			validData, ok := args[len(args)-2].(bool)
			if !ok {
				return fmt.Errorf("when arg is not a bool but got %v", args[len(args)-2]), false
			}
			size := 1
			if l >= 2 {
				size, err = cast.ToInt(args[1], cast.STRICT)
				if err != nil {
					return fmt.Errorf("error converting second arg %v to int: %v", args[1], err), false
				}
			}
			var dftVal interface{} = nil
			if l >= 3 {
				dftVal = args[2]
			}
			ignoreNull := true
			if l >= 4 {
				ignoreNull, ok = args[3].(bool)
				if !ok {
					return fmt.Errorf("The fourth arg is not a bool but got %v", args[0]), false
				}
			}
			var rq *ringqueue = nil
			var rtnVal interface{} = nil
			// first time call, need create state for lag
			if v == nil {
				rq = newRingqueue(size)
				rq.fill(dftVal)
				err := ctx.PutState(key, rq)
				if err != nil {
					return fmt.Errorf("error setting state for %s: %v", key, err), false
				}
			} else {
				rq, _ = v.(*ringqueue)
			}
			rtnVal, _ = rq.peek()
			if validData {
				if !ignoreNull || args[0] != nil {
					rtnVal, _ = rq.fetch()
					rq.append(args[0])
					err := ctx.PutState(key, rq)
					if err != nil {
						return fmt.Errorf("error setting state for %s: %v", key, err), false
					}
				}
			}
			return rtnVal, true
		},
		val: func(_ api.FunctionContext, args []ast.Expr) error {
			l := len(args)
			if l != 1 && l != 2 && l != 3 && l != 4 {
				return fmt.Errorf("expect one two or three args but got %d", l)
			}
			if l >= 2 {
				if ast.IsFloatArg(args[1]) || ast.IsTimeArg(args[1]) || ast.IsBooleanArg(args[1]) || ast.IsStringArg(args[1]) || ast.IsFieldRefArg(args[1]) {
					return ProduceErrInfo(1, "int")
				}
				if s, ok := args[1].(*ast.IntegerLiteral); ok {
					if s.Val < 0 {
						return fmt.Errorf("the index should not be a nagtive integer")
					}
				}
			}
			if l == 4 {
				if ast.IsNumericArg(args[3]) || ast.IsTimeArg(args[3]) || ast.IsStringArg(args[3]) {
					return ProduceErrInfo(3, "bool")
				}
			}
			return nil
		},
	}

	builtins["latest"] = builtinFunc{
		fType: ast.FuncTypeScalar,
		exec: func(ctx api.FunctionContext, args []interface{}) (interface{}, bool) {
			l := len(args) - 2
			if l != 1 && l != 2 {
				return fmt.Errorf("expect one or two args but got %d", l), false
			}
			paraLen := len(args) - 2
			key := args[len(args)-1].(string)
			validData, ok := args[len(args)-2].(bool)
			if !ok {
				return fmt.Errorf("when arg is not a bool but got %v", args[len(args)-2]), false
			}
			// notice nil is ignored in latest
			if validData && args[0] != nil {
				ctx.PutState(key, args[0])
				return args[0], true
			} else {
				v, err := ctx.GetState(key)
				if err != nil {
					return fmt.Errorf("error getting state for %s: %v", key, err), false
				}
				if v == nil {
					if paraLen == 2 {
						return args[1], true
					} else {
						return nil, true
					}
				} else {
					return v, true
				}
			}
		},
		val: func(_ api.FunctionContext, args []ast.Expr) error {
			l := len(args)
			if l != 1 && l != 2 {
				return fmt.Errorf("expect one or two args but got %d", l)
			}
			return nil
		},
	}
}

func getMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func getMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
