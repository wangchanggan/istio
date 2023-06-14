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

package controller

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"istio.io/istio/pilot/pkg/features"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pkg/config"
	"istio.io/istio/pkg/config/host"
	"istio.io/istio/pkg/config/schema/kind"
	"istio.io/istio/pkg/util/sets"
)

// Pilot can get EDS information from Kubernetes from two mutually exclusive sources, Endpoints and
// EndpointSlices. The kubeEndpointsController abstracts these details and provides a common interface
// that both sources implement.
type kubeEndpointsController interface {
	HasSynced() bool
	// sync triggers a re-sync. This can be set with:
	// * name+namespace: sync a single object
	// * namespace: sync that namespace
	// * neither: sync all namespaces
	sync(name, ns string, event model.Event, filtered bool) error
	InstancesByPort(c *Controller, svc *model.Service, reqSvcPort int) []*model.ServiceInstance
	GetProxyServiceInstances(c *Controller, proxy *model.Proxy) []*model.ServiceInstance
	buildIstioEndpoints(ep any, host host.Name) []*model.IstioEndpoint
	buildIstioEndpointsWithService(name, namespace string, host host.Name, clearCache bool) []*model.IstioEndpoint
	// forgetEndpoint does internal bookkeeping on a deleted endpoint
	forgetEndpoint(endpoint any) map[host.Name][]*model.IstioEndpoint
	getServiceNamespacedName(ep any) types.NamespacedName
}

// processEndpointEvent triggers the config update.
// Endpoint资源：主要根据集群所使用的Endpoint模式的不同，将Endpoint交由Endpoint控制器处理，将Endpointslice交由EndpointSlice控制器处理。
// Endpoint事件处理函数没有在外部注册，而是由内置函数调用XDSUpdater接口完成。
// 对Headless服务的Endpoint的处理比较特殊，这里会触发全量的xDS更新。
// 与普通的ClusterlP类型的服务共享一个监听器不同，Headless服务的xDS模型比较特殊，它的每个服务实例都可能对应一个监听器。
// 在这种情况下，Headless服务实例的更新必然引起xDS监听器的变化。
// Endpoint的处理函数内置，不需要从外部注册，这是因为Istio 1.0引入了增量EDS的优化，目前增量EDS只支持Kubernetes平台。
// 它根据Endpoint 的变化更新与服务相关的缓存，并判断本次Endpoint资源的更新是否需要全量的xDS配置分发。
// 在服务网格中变化最多、最快的往往是Endpoint，因此增量EDS的更新能够大大降低系统的资源(CPU、内存、带宽)开销，提高服务网格的稳定性。
func processEndpointEvent(c *Controller, epc kubeEndpointsController, name string, namespace string, event model.Event, ep any) error {
	// Update internal endpoint cache no matter what kind of service, even headless service.
	// As for gateways, the cluster discovery type is `EDS` for headless service.
	// 更新Endpoint缓存，触发xDS更新
	updateEDS(c, epc, ep, event)
	if svc := c.services.Get(name, namespace); svc != nil {
		// if the service is headless service, trigger a full push if EnableHeadlessService is true,
		// otherwise push endpoint updates - needed for NDS output.
		// 如果是Headless服务，则触发全量的xDS更新
		if svc.Spec.ClusterIP == v1.ClusterIPNone {
			for _, modelSvc := range c.servicesForNamespacedName(config.NamespacedName(svc)) {
				c.opts.XDSUpdater.ConfigUpdate(&model.PushRequest{
					Full: features.EnableHeadlessService,
					// TODO: extend and set service instance type, so no need to re-init push context
					ConfigsUpdated: sets.New(model.ConfigKey{Kind: kind.ServiceEntry, Name: modelSvc.Hostname.String(), Namespace: svc.Namespace}),

					Reason: []model.TriggerReason{model.HeadlessEndpointUpdate},
				})
				return nil
			}
		}
	}

	return nil
}

func updateEDS(c *Controller, epc kubeEndpointsController, ep any, event model.Event) {
	namespacedName := epc.getServiceNamespacedName(ep)
	log.Debugf("Handle EDS endpoint %s %s in namespace %s", namespacedName.Name, event, namespacedName.Namespace)
	var forgottenEndpointsByHost map[host.Name][]*model.IstioEndpoint
	if event == model.EventDelete {
		forgottenEndpointsByHost = epc.forgetEndpoint(ep)
	}

	shard := model.ShardKeyFromRegistry(c)

	for _, hostName := range c.hostNamesForNamespacedName(namespacedName) {
		var endpoints []*model.IstioEndpoint
		if forgottenEndpointsByHost != nil {
			endpoints = forgottenEndpointsByHost[hostName]
		} else {
			// 生成IstioEndpoint
			endpoints = epc.buildIstioEndpoints(ep, hostName)
		}

		// Kubernetes服务选择Workload Entries，支持混合部署
		if features.EnableK8SServiceSelectWorkloadEntries {
			svc := c.GetService(hostName)
			if svc != nil {
				fep := c.collectWorkloadInstanceEndpoints(svc)
				endpoints = append(endpoints, fep...)
			} else {
				log.Debugf("Handle EDS endpoint: skip collecting workload entry endpoints, service %s/%s has not been populated",
					namespacedName.Namespace, namespacedName.Name)
			}
		}

		// 调用EDSUpdate进行EDS的缓存更新及触发xDS更新，XDSServer实现了XDSUpdater接口。
		c.opts.XDSUpdater.EDSUpdate(shard, string(hostName), namespacedName.Namespace, endpoints)
	}
}

// getPod fetches a pod by name or IP address.
// A pod may be missing (nil) for two reasons:
//   - It is an endpoint without an associated Pod. In this case, expectPod will be false.
//   - It is an endpoint with an associate Pod, but its not found. In this case, expectPod will be true.
//     this may happen due to eventually consistency issues, out of order events, etc. In this case, the caller
//     should not precede with the endpoint, or inaccurate information would be sent which may have impacts on
//     correctness and security.
//
// Note: this is only used by endpoints and endpointslice controller
func getPod(c *Controller, ip string, ep *metav1.ObjectMeta, targetRef *v1.ObjectReference, host host.Name) (*v1.Pod, bool) {
	var expectPod bool
	pod := c.getPod(ip, ep.Namespace, targetRef)
	if targetRef != nil && targetRef.Kind == "Pod" {
		expectPod = true
		if pod == nil {
			c.registerEndpointResync(ep, ip, host)
		}
	}

	return pod, expectPod
}

func (c *Controller) registerEndpointResync(ep *metav1.ObjectMeta, ip string, host host.Name) {
	// This means, the endpoint event has arrived before pod event.
	// This might happen because PodCache is eventually consistent.
	log.Debugf("Endpoint without pod %s %s.%s", ip, ep.Name, ep.Namespace)
	endpointsWithNoPods.Increment()
	if c.opts.Metrics != nil {
		c.opts.Metrics.AddMetric(model.EndpointNoPod, string(host), "", ip)
	}
	// Tell pod cache we want to queue the endpoint event when this pod arrives.
	c.pods.queueEndpointEventOnPodArrival(config.NamespacedName(ep), ip)
}

// getPod fetches a pod by name or IP address.
// A pod may be missing (nil) for two reasons:
// * It is an endpoint without an associated Pod.
// * It is an endpoint with an associate Pod, but its not found.
func (c *Controller) getPod(ip string, namespace string, targetRef *v1.ObjectReference) *v1.Pod {
	if targetRef != nil && targetRef.Kind == "Pod" {
		key := types.NamespacedName{Name: targetRef.Name, Namespace: targetRef.Namespace}
		pod := c.pods.getPodByKey(key)
		return pod
	}

	// This means the endpoint is manually controlled
	// TODO: this may be not correct because of the hostnetwork pods may have same ip address
	// Do we have a way to get the pod from only endpoint?
	pod := c.pods.getPodByIP(ip)
	if pod != nil {
		// This prevents selecting a pod in another different namespace
		if pod.Namespace != namespace {
			pod = nil
		}
	}
	// There maybe no pod at all
	return pod
}
