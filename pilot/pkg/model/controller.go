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

package model

import (
	"sync"

	"istio.io/istio/pkg/cluster"
)

type ServiceHandler func(*Service, *Service, Event)

// Controller defines an event controller loop.  Proxy agent registers itself
// with the controller loop and receives notifications on changes to the
// service topology or changes to the configuration artifacts.
//
// The controller guarantees the following consistency requirement: registry
// view in the controller is as AT LEAST as fresh as the moment notification
// arrives, but MAY BE more fresh (e.g. "delete" cancels an "add" event).  For
// example, an event for a service creation will see a service registry without
// the service if the event is immediately followed by the service deletion
// event.
//
// Handlers execute on the single worker queue in the order they are appended.
// Handlers receive the notification event and the associated object.  Note
// that all handlers must be appended before starting the controller.
// 控制器接口，用于注册事件处理回调函数。具体的注册中心控制器会接收资源更新事件，并执行相应的事件处理回调函数。
type Controller interface {
	// Note: AppendXXXHandler is used to register high level handlers.
	// For per cluster handlers, they should be registered by the `AppendXXXHandlerForCluster` interface.

	// AppendServiceHandler notifies about changes to the service catalog.
	// 注册服务的事件处理回调函数
	AppendServiceHandler(f ServiceHandler)

	// AppendWorkloadHandler notifies about changes to workloads. This differs from InstanceHandler,
	// which deals with service instances (the result of a merge of Service and Workload)
	// 注册服务实例的事件处理回调函数，主要是为了支持Kubernetes Service和Istio ServiceEntry交叉选择服务实例
	AppendWorkloadHandler(f func(*WorkloadInstance, Event))

	// Run until a signal is received
	// 运行控制器
	Run(stop <-chan struct{})

	// HasSynced returns true after initial cache synchronization is complete
	// 同步检查控制器的缓存
	HasSynced() bool
}

// AggregateController is a wrapper of Controller, it supports registering handlers of a specific cluster。
type AggregateController interface {
	Controller
	// AppendServiceHandlerForCluster is similar to Controller.AppendServiceHandler,
	// but it is used to store the handler from a specific cluster.
	AppendServiceHandlerForCluster(clusterID cluster.ID, f ServiceHandler)
	// AppendWorkloadHandlerForCluster is similar to Controller.AppendWorkloadHandler,
	// but it is used to store the handler from a specific cluster.
	AppendWorkloadHandlerForCluster(clusterID cluster.ID, f func(*WorkloadInstance, Event))
	UnRegisterHandlersForCluster(clusterID cluster.ID)
}

// ControllerHandlers is a utility to help Controller implementations manage their lists of handlers.
type ControllerHandlers struct {
	mutex            sync.RWMutex
	serviceHandlers  []ServiceHandler
	workloadHandlers []func(*WorkloadInstance, Event)
}

func (c *ControllerHandlers) AppendServiceHandler(f ServiceHandler) {
	// Copy on write.
	c.mutex.Lock()
	handlers := make([]ServiceHandler, 0, len(c.serviceHandlers)+1)
	handlers = append(handlers, c.serviceHandlers...)
	handlers = append(handlers, f)
	c.serviceHandlers = handlers
	c.mutex.Unlock()
}

func (c *ControllerHandlers) AppendWorkloadHandler(f func(*WorkloadInstance, Event)) {
	// Copy on write.
	c.mutex.Lock()
	handlers := make([]func(*WorkloadInstance, Event), 0, len(c.workloadHandlers)+1)
	handlers = append(handlers, c.workloadHandlers...)
	handlers = append(handlers, f)
	c.workloadHandlers = handlers
	c.mutex.Unlock()
}

func (c *ControllerHandlers) GetServiceHandlers() []ServiceHandler {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	// Return a shallow copy of the array
	return c.serviceHandlers
}

func (c *ControllerHandlers) GetWorkloadHandlers() []func(*WorkloadInstance, Event) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	// Return a shallow copy of the array
	return c.workloadHandlers
}

func (c *ControllerHandlers) NotifyServiceHandlers(prev, curr *Service, event Event) {
	for _, f := range c.GetServiceHandlers() {
		f(prev, curr, event)
	}
}

func (c *ControllerHandlers) NotifyWorkloadHandlers(w *WorkloadInstance, event Event) {
	for _, f := range c.GetWorkloadHandlers() {
		f(w, event)
	}
}

// Event represents a registry update event
type Event int

const (
	// EventAdd is sent when an object is added
	EventAdd Event = iota

	// EventUpdate is sent when an object is modified
	// Captures the modified object
	EventUpdate

	// EventDelete is sent when an object is deleted
	// Captures the object at the last known state
	EventDelete
)

func (event Event) String() string {
	out := "unknown"
	switch event {
	case EventAdd:
		out = "add"
	case EventUpdate:
		out = "update"
	case EventDelete:
		out = "delete"
	}
	return out
}
