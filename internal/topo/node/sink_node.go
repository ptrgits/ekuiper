// Copyright 2024-2025 EMQ Technologies Co., Ltd.
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

package node

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lf-edge/ekuiper/contract/v2/api"

	"github.com/lf-edge/ekuiper/v2/internal/pkg/def"
	kctx "github.com/lf-edge/ekuiper/v2/internal/topo/context"
	"github.com/lf-edge/ekuiper/v2/internal/xsql"
	"github.com/lf-edge/ekuiper/v2/pkg/errorx"
	"github.com/lf-edge/ekuiper/v2/pkg/infra"
	"github.com/lf-edge/ekuiper/v2/pkg/model"
	"github.com/lf-edge/ekuiper/v2/pkg/timex"
)

// SinkNode represents a sink node that collects data from the stream
// It typically only do connect and send. It does not do any processing.
// This node is the skeleton. It will refer to a sink instance to do the real work.
type SinkNode struct {
	*defaultSinkNode
	sink           api.Sink
	eoflimit       int
	currentEof     int
	resendInterval time.Duration
	doCollect      func(ctx api.StreamContext, sink api.Sink, data any) error
	// channel for resend
	resendOut chan<- any
}

// Caching:
// 1. Set cache settings to enable diskCache
// 2. Set resendInterval and bufferLength will use bufferLength as the memory cache
// 3. By default, drop if it cannot sends out.
func newSinkNode(ctx api.StreamContext, name string, rOpt def.RuleOption, eoflimit int, sc *SinkConf, isRetry bool) *SinkNode {
	// set collect retry according to cache setting
	retry := time.Duration(sc.ResendInterval)
	if (sc.EnableCache || isRetry) && retry <= 0 {
		// default retry interval to 100ms
		retry = 100 * time.Millisecond
	}
	// Sink input channel as buffer
	if isRetry || (sc.EnableCache && !sc.ResendAlterQueue) {
		rOpt.BufferLength = sc.MemoryCacheThreshold
	} else {
		rOpt.BufferLength = sc.BufferLength
	}
	ctx.GetLogger().Infof("create sink node %s with isRetry %v, resendInterval %d, bufferLength %d", name, isRetry, retry, rOpt.BufferLength)
	return &SinkNode{
		defaultSinkNode: newDefaultSinkNode(name, &rOpt),
		eoflimit:        eoflimit,
		resendInterval:  retry,
	}
}

func (s *SinkNode) setKafkaSinkStatsManager(ctx api.StreamContext) {
	if strings.Contains(strings.ToLower(s.name), "kafka") {
		dctx, ok := ctx.(*kctx.DefaultContext)
		if ok {
			kctx.WithValue(dctx, "$statManager", s.statManager)
			s.isStatManagerHostBySink = true
		}
	}
}

func (s *SinkNode) Exec(ctx api.StreamContext, errCh chan<- error) {
	s.prepareExec(ctx, errCh, "sink")
	go func() {
		err := infra.SafeRun(func() error {
			s.setKafkaSinkStatsManager(ctx)
			err := s.sink.Connect(ctx, s.connectionStatusChange)
			if err != nil {
				infra.DrainError(ctx, err, errCh)
			}
			defer func() {
				s.sink.Close(ctx)
				s.Close()
			}()
			s.currentEof = 0
			for {
				select {
				case <-ctx.Done():
					return nil
				case d := <-s.input:
					data, processed := s.ingest(ctx, d)
					if processed {
						break
					}
					s.onProcessStart(ctx, data)
					err = s.doCollect(ctx, s.sink, data)
					if err != nil { // resend handling when enabling cache. Two cases: 1. send to alter queue with resendOUt. 2. retry (blocking) until success or unrecoverable error if resendInterval is set
						s.onError(ctx, err)
						if s.resendOut != nil {
							s.BroadcastCustomized(data, func(val any) {
								select {
								case s.resendOut <- val:
									// do nothing
								case <-ctx.Done():
									// rule stop so stop waiting
								default:
									s.onError(ctx, fmt.Errorf("buffer full, drop message from %s to resend sink", s.name))
								}
							})
						} else if s.resendInterval > 0 {
							if !errorx.IsIOError(err) {
								ctx.GetLogger().Errorf("no io error %v, drop %v", err, xsql.GetId(data))
							} else {
								ticker := timex.GetTicker(s.resendInterval)
								defer ticker.Stop()
								for err != nil && errorx.IsIOError(err) {
									ctx.GetLogger().Debugf("wait resending %v", xsql.GetId(data))
									select {
									case <-ctx.Done():
										ctx.GetLogger().Infof("rule stop, exit retry for %v", xsql.GetId(data))
										return nil
									case <-ticker.C:
										err = s.doCollect(ctx, s.sink, data)
										s.statManager.SetBufferLength(int64(len(s.input)))
									}
								}
								if err == nil {
									ctx.GetLogger().Debugf("resend success %v", xsql.GetId(data))
									s.onSend(ctx, data)
								} else {
									ctx.GetLogger().Debugf("no io error %v", err)
								}
							}
						}
					} else {
						s.onSend(ctx, data)
					}
					s.onProcessEnd(ctx)
					s.statManager.SetBufferLength(int64(len(s.input)))
				}
			}
		})
		if err != nil {
			infra.DrainError(ctx, err, errCh)
		}
	}()
}

func (s *SinkNode) SetResendOutput(output chan<- any) {
	s.resendOut = output
}

func (s *SinkNode) connectionStatusChange(status string, message string) {
	if status == api.ConnectionDisconnected {
		s.statManager.IncTotalExceptions(message)
	}
	s.statManager.SetConnectionState(status, message)
}

func (s *SinkNode) ingest(ctx api.StreamContext, item any) (any, bool) {
	ctx.GetLogger().Debugf("%s_%d receive %v", ctx.GetOpId(), ctx.GetInstanceId(), item)
	item, processed := s.preprocess(ctx, item)
	if processed {
		return item, processed
	}
	switch d := item.(type) {
	case error:
		if s.sendError {
			return d, false
		}
		return nil, true
	case *xsql.WatermarkTuple, xsql.BatchEOFTuple:
		return nil, true
	case xsql.EOFTuple:
		s.currentEof++
		if s.eoflimit == s.currentEof {
			infra.DrainError(ctx, errorx.NewEOF(string(d)), s.ctrlCh)
		}
		return nil, true
	}
	ctx.GetLogger().Debugf("%s_%d receive data %v", ctx.GetOpId(), ctx.GetInstanceId(), item)
	return item, false
}

// NewBytesSinkNode creates a sink node that collects data from the stream. Do some static validation
func NewBytesSinkNode(ctx api.StreamContext, name string, sink api.BytesCollector, rOpt def.RuleOption, eoflimit int, sc *SinkConf, isRetry bool) (*SinkNode, error) {
	ctx.GetLogger().Infof("create bytes sink node %s", name)
	n := newSinkNode(ctx, name, rOpt, eoflimit, sc, isRetry)
	n.sink = sink
	n.doCollect = bytesCollect
	return n, nil
}

func bytesCollect(ctx api.StreamContext, sink api.Sink, data any) (err error) {
	ctx.GetLogger().Debugf("Sink node %s receive data %s", ctx.GetOpId(), data)
	switch d := data.(type) {
	case api.RawTuple:
		err = sink.(api.BytesCollector).Collect(ctx, d)
	case error:
		err = sink.(api.BytesCollector).Collect(ctx, &xsql.RawTuple{
			Rawdata: []byte(d.Error()),
		})
	default:
		err = fmt.Errorf("expect api.RawTuple data type but got %T", d)
	}
	return err
}

// NewTupleSinkNode creates a sink node that collects data from the stream. Do some static validation
func NewTupleSinkNode(ctx api.StreamContext, name string, sink api.TupleCollector, rOpt def.RuleOption, eoflimit int, sc *SinkConf, isRetry bool) (*SinkNode, error) {
	ctx.GetLogger().Infof("create message sink node %s", name)
	n := newSinkNode(ctx, name, rOpt, eoflimit, sc, isRetry)
	n.sink = sink
	n.doCollect = tupleCollect
	return n, nil
}

// return error that cannot be sent
func tupleCollect(ctx api.StreamContext, sink api.Sink, data any) (err error) {
	switch d := data.(type) {
	// Some tuple list type also implements tuple. So need to handle list firstly
	case api.MessageTupleList:
		err = sink.(api.TupleCollector).CollectList(ctx, d)
	case api.MessageTuple:
		err = sink.(api.TupleCollector).Collect(ctx, d)
	case *xsql.RawTuple: // may receive raw tuple from data template
		var message map[string]any
		err = json.Unmarshal(d.Rawdata, &message)
		if err != nil {
			return err
		}
		t := &xsql.Tuple{
			Ctx:       d.Ctx,
			Metadata:  d.Metadata,
			Timestamp: d.Timestamp,
			Emitter:   d.Emitter,
			Props:     d.Props,
			Message:   message,
		}
		err = sink.(api.TupleCollector).Collect(ctx, t)
	case error:
		err = sink.(api.TupleCollector).Collect(ctx, model.NewDefaultSourceTuple(xsql.Message{"error": d.Error()}, nil, timex.GetNow()))
	default:
		err = fmt.Errorf("expect tuple data type but got %T", d)
	}
	return err
}

var _ DataSinkNode = (*SinkNode)(nil)
