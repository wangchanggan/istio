// Copyright Istio Authors
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

package sds

import (
	"net"
	"time"

	"go.uber.org/atomic"
	"google.golang.org/grpc"

	mesh "istio.io/api/mesh/v1alpha1"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pkg/config/schema/kind"
	"istio.io/istio/pkg/security"
	"istio.io/istio/pkg/uds"
	"istio.io/istio/pkg/util/sets"
)

const (
	maxStreams    = 100000
	maxRetryTimes = 5
)

// Server is the gPRC server that exposes SDS through UDS.
type Server struct {
	workloadSds *sdsservice

	grpcWorkloadListener net.Listener

	grpcWorkloadServer *grpc.Server
	stopped            *atomic.Bool
}

// NewServer creates and starts the Grpc server for SDS.
// 创建用于接收Envoy进程发送的SDS请求的sdsServer
func NewServer(options *security.Options, workloadSecretCache security.SecretManager, pkpConf *mesh.PrivateKeyProvider) *Server {
	s := &Server{stopped: atomic.NewBool(false)}
	s.workloadSds = newSDSService(workloadSecretCache, options, pkpConf)
	s.initWorkloadSdsService()
	return s
}

// 根据资源名称resourceName重新创建PushRequest请求并模拟SDS请求从Envoy进程发出的行为
// 之后将该SDS请求发送到UDS通道对应的xdsServer，从而该SDS请求可以经过相同的路径被Pilot-agent进程的StreamSecret回调方法接收和处理
// 之后经历相同的证书生成过程，得到新生成的证书，将其保存到本地环境并添加到证书轮转监控队列后发送到Envoy进程。
func (s *Server) OnSecretUpdate(resourceName string) {
	if s.workloadSds == nil {
		return
	}
	s.workloadSds.XdsServer.Push(&model.PushRequest{
		Full:           false,
		ConfigsUpdated: sets.New(model.ConfigKey{Kind: kind.Secret, Name: resourceName}),
		// 证书轮转标记
		Reason: []model.TriggerReason{model.SecretTrigger},
	})
}

// Stop closes the gRPC server and debug server.
func (s *Server) Stop() {
	if s == nil {
		return
	}
	s.stopped.Store(true)
	if s.grpcWorkloadServer != nil {
		s.grpcWorkloadServer.Stop()
	}
	if s.grpcWorkloadListener != nil {
		s.grpcWorkloadListener.Close()
	}
	if s.workloadSds != nil {
		s.workloadSds.Close()
	}
}

// 对xDS服务器进行初始化并绑定监听器，之后就可以接收SDS请求了
func (s *Server) initWorkloadSdsService() {
	// 创建gRPC协议处理服务器
	s.grpcWorkloadServer = grpc.NewServer(s.grpcServerOptions()...)
	// 将gRPC服务器与sdsServer绑定
	s.workloadSds.register(s.grpcWorkloadServer)
	var err error
	// 创建/etc/istio/proxy/SDS UDS监听
	s.grpcWorkloadListener, err = uds.NewListener(security.WorkloadIdentitySocketPath)
	go func() {
		sdsServiceLog.Info("Starting SDS grpc server")
		waitTime := time.Second
		started := false
		for i := 0; i < maxRetryTimes; i++ {
			if s.stopped.Load() {
				return
			}
			serverOk := true
			setUpUdsOK := true
			if s.grpcWorkloadListener == nil {
				if s.grpcWorkloadListener, err = uds.NewListener(security.WorkloadIdentitySocketPath); err != nil {
					sdsServiceLog.Errorf("SDS grpc server for workload proxies failed to set up UDS: %v", err)
					setUpUdsOK = false
				}
			}
			if s.grpcWorkloadListener != nil {
				// 运行sdsServer处理SDS请求
				if err = s.grpcWorkloadServer.Serve(s.grpcWorkloadListener); err != nil {
					sdsServiceLog.Errorf("SDS grpc server for workload proxies failed to start: %v", err)
					serverOk = false
				}
			}
			if serverOk && setUpUdsOK {
				sdsServiceLog.Infof("SDS server for workload certificates started, listening on %q", security.WorkloadIdentitySocketPath)
				started = true
				break
			}
			time.Sleep(waitTime)
			waitTime *= 2
		}
		if !started {
			sdsServiceLog.Warn("SDS grpc server could not be started")
		}
	}()
}

func (s *Server) grpcServerOptions() []grpc.ServerOption {
	grpcOptions := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(maxStreams)),
	}

	return grpcOptions
}
