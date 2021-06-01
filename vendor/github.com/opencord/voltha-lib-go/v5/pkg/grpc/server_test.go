/*
 * Copyright 2018-present Open Networking Foundation

 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at

 * http://www.apache.org/licenses/LICENSE-2.0

 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package grpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// A Mock Probe that returns the Ready member using the IsReady() func
type MockReadyProbe struct {
	Ready bool
}

func (m *MockReadyProbe) IsReady() bool {
	return m.Ready
}

// A Mock handler that returns the request as its result
func MockUnaryHandler(ctx context.Context, req interface{}) (interface{}, error) {
	_ = ctx
	return req, nil
}

func TestNewGrpcServer(t *testing.T) {
	server := NewGrpcServer("127.0.0.1:1234", nil, false, nil)
	assert.NotNil(t, server)
}

func TestMkServerInterceptorNoProbe(t *testing.T) {
	server := NewGrpcServer("127.0.0.1:1234", nil, false, nil)
	assert.NotNil(t, server)

	f := mkServerInterceptor(server)
	assert.NotNil(t, f)

	req := "SomeRequest"
	serverInfo := grpc.UnaryServerInfo{Server: nil, FullMethod: "somemethod"}

	result, err := f(context.Background(),
		req,
		&serverInfo,
		MockUnaryHandler)

	assert.Nil(t, err)
	assert.Equal(t, "SomeRequest", result)
}

func TestMkServerInterceptorReady(t *testing.T) {
	probe := &MockReadyProbe{Ready: true}

	server := NewGrpcServer("127.0.0.1:1234", nil, false, probe)
	assert.NotNil(t, server)

	f := mkServerInterceptor(server)
	assert.NotNil(t, f)

	req := "SomeRequest"
	serverInfo := grpc.UnaryServerInfo{Server: nil, FullMethod: "somemethod"}

	result, err := f(context.Background(),
		req,
		&serverInfo,
		MockUnaryHandler)

	assert.Nil(t, err)
	assert.NotNil(t, result)
}

func TestMkServerInterceptorNotReady(t *testing.T) {
	probe := &MockReadyProbe{Ready: false}

	server := NewGrpcServer("127.0.0.1:1234", nil, false, probe)
	assert.NotNil(t, server)

	f := mkServerInterceptor(server)
	assert.NotNil(t, f)

	req := "SomeRequest"
	serverInfo := grpc.UnaryServerInfo{Server: nil, FullMethod: "somemethod"}

	result, err := f(context.Background(),
		req,
		&serverInfo,
		MockUnaryHandler)

	assert.NotNil(t, err)
	assert.Nil(t, result)
}

// prettyPrintDeviceUpdateLog is used just for debugging and exploring the Core logs
func prettyPrintDeviceUpdateLog(inputFile string, filter string) {
	file, err := os.Open(inputFile)
	if err != nil {
		logger.Fatal(context.Background(), err)
	}
	defer file.Close()

	var logEntry struct {
		Level  string  `json:"level"`
		Ts     string  `json:"ts"`
		Caller string  `json:"caller"`
		Msg    string  `json:"msg"`
		Key    string  `json:"key"`
		Path   string  `json:"path"`
		Time   float64 `json:"time"`
		Error  string  `json:"error"`
	}

	keys := make(map[string]int)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		input := scanner.Text()
		// Look for device update logs only
		if !strings.Contains(input, filter) {
			continue
		}

		if err := json.Unmarshal([]byte(input), &logEntry); err != nil {
			logger.Fatal(context.Background(), err)
		}
		fmt.Println(
			fmt.Sprintf(
				"%s\t%s\t%s\t%0.2f",
				logEntry.Ts,
				logEntry.Msg,
				logEntry.Path,
				logEntry.Time*1000,
			))
		if _, ok := keys[logEntry.Path]; ok {
			keys[logEntry.Path] += 1
		} else {
			keys[logEntry.Path] = 1
		}
	}

	fmt.Printf("\n\n")
	type kv struct {
		Key   string
		Value int
		//time []float64
	}

	var ss []kv
	for k, v := range keys {
		ss = append(ss, kv{k, v})
	}

	sort.Slice(ss, func(i, j int) bool {
		return ss[i].Value > ss[j].Value
	})

	for _, kv := range ss {
		fmt.Printf("%s\t%d\n", kv.Key, kv.Value)
	}

	//for _, kv := range ss {
	//	for k, v := range keys {
	//		if keys[k] == kv.Value {
	//			kv.time = keys[]
	//		}
	//	}
	//	fmt.Printf("%s, %d\n", kv.Key, kv.Value)
	//}
}

// prettyPrintDeviceUpdateLog is used just for debugging and exploring the Core logs
func prettyPrintDeviceUpdateLogCount(inputFiles []string) {

	type allOutput struct {
		core_get int
		core_put int
		onu_get  int
		onu_put  int
		olt_get  int
		olt_put  int
	}

	var logEntry struct {
		Level  string  `json:"level"`
		Ts     string  `json:"ts"`
		Caller string  `json:"caller"`
		Msg    string  `json:"msg"`
		Key    string  `json:"key"`
		Path   string  `json:"path"`
		Time   float64 `json:"time"`
		Error  string  `json:"error"`
	}

	out := map[string]allOutput{}
	//out := map[string][]int{}
	for _, inputFile := range inputFiles {
		component := ""
		if strings.Contains(inputFile, "core") {
			component = "core"
		} else if strings.Contains(inputFile, "onu") {
			component = "onu"
		} else if strings.Contains(inputFile, "olt") {
			component = "olt"
		} else {
			continue
		}
		file, err := os.Open(inputFile)
		if err != nil {
			logger.Fatal(context.Background(), err)
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			input := scanner.Text()

			if !strings.Contains(input, "put-key") && !strings.Contains(input, "get-key") {
				continue
			}
			//if !strings.Contains(input, "put-key") {
			//	continue
			//}

			if err := json.Unmarshal([]byte(input), &logEntry); err != nil {
				logger.Fatal(context.Background(), err)
			}

			// Get time in second only
			ts, _ := time.Parse(time.RFC3339, logEntry.Ts)
			tsStr := ts.Format("2006-01-02T15:04:05")
			//if _, ok := out[tsStr]; !ok {
			//	out[tsStr] = allOutput{}
			//}

			//fmt.Println(tsStr)
			//fmt.Println(len(out), tsStr)

			switch logEntry.Msg {
			case "put-key":
				{
					if component == "core" {
						//fmt.Println("put-key")
						if _, ok := out[tsStr]; ok {
							t := out[tsStr]
							t.core_put++
							out[tsStr] = t
						} else {
							out[tsStr] = allOutput{core_put: 1}
						}
					} else if component == "olt" {
						if _, ok := out[tsStr]; ok {
							t := out[tsStr]
							t.olt_put++
							out[tsStr] = t
						} else {
							out[tsStr] = allOutput{olt_put: 1}
						}
					} else if component == "onu" {
						if _, ok := out[tsStr]; ok {
							t := out[tsStr]
							t.onu_put++
							out[tsStr] = t
						} else {
							out[tsStr] = allOutput{onu_put: 1}
						}
					} else {
						continue
					}
				}
			case "get-key":
				{
					if component == "core" {
						//fmt.Println("get-key")
						if _, ok := out[tsStr]; ok {
							t := out[tsStr]
							t.core_get++
							out[tsStr] = t
						} else {
							out[tsStr] = allOutput{core_get: 1}
						}
					} else if component == "olt" {
						if _, ok := out[tsStr]; ok {
							t := out[tsStr]
							t.olt_get++
							out[tsStr] = t
						} else {
							out[tsStr] = allOutput{olt_get: 1}
						}
					} else if component == "onu" {
						if _, ok := out[tsStr]; ok {
							t := out[tsStr]
							t.onu_get++
							out[tsStr] = t
						} else {
							out[tsStr] = allOutput{onu_get: 1}
						}
					} else {
						continue
					}
				}
			default:
				continue
			}
		}
	}

	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("Timestamp\tCore-get\tCore-put\tOpenOlt-get\tOpenOlt-put\tOpenOnu-get\tOpenOnu-put")
	for _, k := range keys {
		fmt.Println(
			fmt.Sprintf(
				"%s\t%d\t%d\t%d\t%d\t%d\t%d",
				k,
				out[k].core_get,
				out[k].core_put,
				out[k].olt_get,
				out[k].olt_put,
				out[k].onu_get,
				out[k].onu_put,
			))

	}

	//for k,v := range out {
	//	fmt.Println(
	//		fmt.Sprintf(
	//			"%s\t%d\t%d\t%d\t%d\t%d\t%d",
	//			k,
	//			v.core_get,
	//			v.core_put,
	//			v.core_get,
	//			v.core_get,
	//			v.core_get,
	//			v.core_get,
	//		))
	//}
}

func TestGenerateCuratedFile(t *testing.T) {
	var inputFile = os.Getenv("INF")
	var filter = os.Getenv("filter")
	////inputFile := "/Users/knursimu/work/Logs/Etcd/withtiming/core.log"
	////filter := "list-key"
	////input1 := []string{"/Users/knursimu/work/Logs/Etcd/withtiming/core.log"}

	//input1 := []string{"/Users/knursimu/work/Logs/Etcd/run-1698/archive/logs/core.log", "/Users/knursimu/work/Logs/Etcd/run-1698/archive/logs/olt.log", "/Users/knursimu/work/Logs/Etcd/run-1698/archive/logs/onu.log"}
	//prettyPrintDeviceUpdateLogCount(input1)
	prettyPrintDeviceUpdateLog(inputFile, filter)

}
