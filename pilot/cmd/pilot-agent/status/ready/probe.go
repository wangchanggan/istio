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

package ready

import (
	"context"
	"fmt"

	admin "github.com/envoyproxy/go-control-plane/envoy/admin/v3"

	"istio.io/istio/pilot/cmd/pilot-agent/metrics"
	"istio.io/istio/pilot/cmd/pilot-agent/status/util"
)

// Probe for readiness.
type Probe struct {
	LocalHostAddr       string
	AdminPort           uint16
	receivedFirstUpdate bool
	// Indicates that Envoy is ready atleast once so that we can cache and reuse that probe.
	atleastOnceReady bool
	Context          context.Context
	// NoEnvoy so we only check config status
	NoEnvoy bool
}

type Prober interface {
	// Check executes the probe and returns an error if the probe fails.
	Check() error
}

var _ Prober = &Probe{}

// Check executes the probe and returns an error if the probe fails.
func (p *Probe) Check() error {
	// First, check that Envoy has received a configuration update from Pilot.
	// 检查Envoy进程是否从Pilot-agent进程接收到了启动配置
	if err := p.checkConfigStatus(); err != nil {
		return err
	}
	return p.isEnvoyReady()
}

// checkConfigStatus checks to make sure initial configs have been received from Pilot.
func (p *Probe) checkConfigStatus() error {
	if p.NoEnvoy {
		// TODO some way to verify XDS proxy -> control plane works
		return nil
	}
	if p.receivedFirstUpdate {
		return nil
	}

	s, err := util.GetUpdateStatusStats(p.LocalHostAddr, p.AdminPort)
	if err != nil {
		return err
	}

	CDSUpdated := s.CDSUpdatesSuccess > 0
	LDSUpdated := s.LDSUpdatesSuccess > 0
	if CDSUpdated && LDSUpdated {
		p.receivedFirstUpdate = true
		return nil
	}

	if !CDSUpdated && !LDSUpdated {
		return fmt.Errorf("config not received from XDS server (is Istiod running?): %s", s.String())
	} else if s.LDSUpdatesRejection > 0 || s.CDSUpdatesRejection > 0 {
		return fmt.Errorf("config received from XDS server, but was rejected: %s", s.String())
	} else {
		return fmt.Errorf("config not fully received from XDS server: %s", s.String())
	}
}

// isEnvoyReady checks to ensure that Envoy is in the LIVE state and workers have started.
func (p *Probe) isEnvoyReady() error {
	if p.NoEnvoy {
		return nil
	}
	if p.Context == nil {
		// 向Envoy进程的Admin 15000端口发送请求，要求检查Envoy进程是否已经处于ready状态
		return p.checkEnvoyReadiness()
	}
	select {
	case <-p.Context.Done():
		return fmt.Errorf("server is not live, current state is: %s", admin.ServerInfo_DRAINING.String())
	default:
		return p.checkEnvoyReadiness()
	}
}

func (p *Probe) checkEnvoyReadiness() error {
	// If Envoy is ready at least once i.e. server state is LIVE and workers
	// have started, they will not go back in the life time of Envoy process.
	// They will only change at hot restart or health check fails. Since istio
	// does not use both of them, it is safe to cache this value. Since the
	// actual readiness probe goes via Envoy, it ensures that Envoy is actively
	// serving traffic and we can rely on that.
	// 如果Envoy进程已经处于ready状态，则不再检查
	if p.atleastOnceReady {
		return nil
	}

	// 向Envoy进程发送检查请求
	err := checkEnvoyStats(p.LocalHostAddr, p.AdminPort)
	if err == nil {
		metrics.RecordStartupTime()
		// 第一次检查Envoy ready设置标志
		p.atleastOnceReady = true
	}
	return err
}

// checkEnvoyStats actually executes the Stats Query on Envoy admin endpoint.
func checkEnvoyStats(host string, port uint16) error {
	// 组装HTTP探测请求
	state, ws, err := util.GetReadinessStats(host, port)
	if err != nil {
		return fmt.Errorf("failed to get readiness stats: %v", err)
	}

	if state != nil && admin.ServerInfo_State(*state) != admin.ServerInfo_LIVE {
		return fmt.Errorf("server is not live, current state is: %v", admin.ServerInfo_State(*state).String())
	}

	if !ws {
		return fmt.Errorf("workers have not yet started")
	}

	return nil
}
