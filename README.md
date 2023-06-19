# Istio源码分析
Source Code From

https://github.com/istio/istio/archive/refs/tags/1.18.0.zip

## 目录
-   [Istio源码分析](#istio源码分析)
    -   [目录](#目录)
    -   [Pilot](#pilot)
        -   [启动流程](#启动流程)
        -   [ConfigController](#configcontroller)
            -   [核心接口](#核心接口)
            -   [初始化](#初始化)
            -   [核心工作机制](#核心工作机制)
        -   [ServiceController](#servicecontroller)
            -   [核心接口](#核心接口-1)
            -   [初始化流程](#初始化流程)
            -   [工作机制](#工作机制)
        -   [xDS的异步分发](#xds的异步分发)
            -   [任务处理函数的注册](#任务处理函数的注册)
            -   [Config控制器的任务处理流程](#config控制器的任务处理流程)
            -   [Service控制器的任务处理流程](#service控制器的任务处理流程)
            -   [资源更新事件处理：xDS分发](#资源更新事件处理xds分发)
        -   [对xDS更新的预处理](#对xds更新的预处理)
            -   [防抖动](#防抖动)
            -   [XDSServer的缓存更新](#xdsserver的缓存更新)
            -   [PushContext（推送上下文）的初始化](#pushcontext推送上下文的初始化)
            -   [Pilot-push事件的发送及井发控制](#pilot-push事件的发送及井发控制)
        -   [xDS配置的生成及分发](#xds配置的生成及分发)
    -   [Citadel](#citadel)
        -   [启动流程](#启动流程-1)
            -   [Istio-CA的创建](#istio-ca的创建)
            -   [SDS服务器的初始化](#sds服务器的初始化)
            -   [Istio-CA的启动](#istio-ca的启动)
        -   [CA服务器的核心原理](#ca服务器的核心原理)
        -   [证书签发实体IstloCA](#证书签发实体istloca)
        -   [CredentialsController](#credentialscontroller)
            -   [创建](#创建)
            -   [核心原理](#核心原理)

## Pilot
Pilot是 Istio控制面的核心组件,它的主要职责有如下两个：

（1）为Sidecar 提供监听器、Route、Cluster、 Endpoint、DNS Name Table等 xDS配置。Pilot在运行时对外提供 gRPC服务，在默认情况下，所有Sidecar代理与Pilot之间都建立了一条gRPC长连接,并且订阅xDS配置。

（2）通过底层平台Kubernetes或者其他注册中心进行服务和配置规则发现,并且实时、动态地进行xDS 生成和分发。

### 启动流程
Pilot组件是由pilot-discovery进程实现的，实际上其他组件如 Citadel、Galley的启动入口都书到了pilot/cmd/pilot-discovery/app/cmd.go中

pilot/cmd/pilot-discovery/app/cmd.go:72

pilot/pkg/bootstrap/server.go:103

### ConfigController
ConfigController（配置资源控制器）主要用于监听注册中心的配置资源，在内存中缓存监听到的所有配置资源，并在 Config 资源更新时调用注册的事件处理函数。由于需要支持多注册中心，因此ConfigController实际上是多个控制器的集合。

#### 核心接口
pilot/pkg/model/config.go:212,153

#### 初始化

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/configController_init.png)

pilot/pkg/bootstrap/configcontroller.go:61,135,346

pilot/pkg/config/kube/crdclient/client.go:61

另外，虽然Istio没有适配器可直接对接其他注册中心，但Istio提供了可扩展的接口协议MCP，方便用户集成其他第三方注册中心。MCP ConfigController与Kubernetes ConfigController基本类似，均实现了ConfigStoreController接口，支持Istio配置资源的发现，并提供了缓存管理功能。在创建MCP ConfigController时需要通过MeshConfig.ConfigSources指定MCP服务器的地址。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/MCP_configController_init.png)

pilot/pkg/bootstrap/configcontroller.go:211

#### 核心工作机制
![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/CRD_operator_process.png)
pilot/pkg/config/kube/crdclient/cache_handler.go:86,40,77

pilot/pkg/bootstrap/server.go:915

完整的Config事件处理流程:

（1）EventHandler构造任务(Task)，任务实际上是对onEvent的封装。

（2）EventHandler将任务推送到ConfigController的任务队列(Task queue)。

（3）任务处理协程阻塞式地读取任务队列，执行任务，通过onEvent方法处理事件，并通过configHandler触发xDS的更新。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/config_event_handling.png)

### ServiceController
ServiceController（服务控制器）是服务发现的核心模块，主要功能是监听底层平台的服务注册中心，将平台服务模型转换成Istio服务模型并缓存；同时根据服务的变化，触发相关服务的事件处理回调函数的执行。

#### 核心接口
ServiceController对外为DiscoveryServer中的XDSServer提供了通用的服务模型查询接口ServiceDiscovery。ServiceController可以同时支持多个服务注册中心，因为它包含不同的注册中心控制器，它们的聚合是通过抽象聚合接口（aggregate.Controller）完成的。

pilot/pkg/serviceregistry/aggregate/controller.go:45,129

pilot/pkg/serviceregistry/instance.go:26

pilot/pkg/model/controller.go:40

pilot/pkg/model/service.go:736

#### 初始化流程

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/serviceController_init.png)

pilot/pkg/serviceregistry/kube/controller/controller.go:224

Kubernetes控制器的核心就是监听Kubernetes相关资源(Service、Endpoint、EndpointSlice、Pod、Node) 的更新事件，执行相应的事件处理回调函数；并且进行从Kubernetes资源对象到Istio资源对象的转换，提供一定的缓存能力，主要是缓存Istio Service与WorkloadInstance。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/k8s_controller_keyAttributes_init.png)

其中，Kubernetes控制器主要负责对4种资源的监听和处理，对于每种类型的资源，控制器分别启动了独立的Informer负责List-Watch, 并且分别注册了不同类型的事件处理函数（onServiceEvent、onPodEvent、onNodeEvent、onEvent）到队列中。

#### 工作机制
ServiceController为4种资源分别创建了Kubernetes Informer，用于监听Kubernetes资源的更新，并为其注册EventHandler。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/serviceController_informer.png)

当监听到Service、Endpoint、Pod、Node资源更新时，EventHandler 会创建资源处理任务并将其推送到任务队列，然后由任务处理协程阻塞式地接收任务对象，最终调用任务处理函数完成对资源对象的事件处理。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/serviceController_event_handling.png)

pilot/pkg/bootstrap/server.go:896

pilot/pkg/serviceregistry/kube/controller/endpointcontroller.go:58

pilot/pkg/serviceregistry/kube/controller/controller.go:352

### xDS的异步分发
#### 任务处理函数的注册
Pilot通过XDSServer处理客户端的订阅请求，并完成xDS配置的生成与下发，而XDSServer的初始化由NewServer完成，因此从实现的角度考虑，将Istio任务处理函数的注册也放在了XDSServer对象的初始化流程中。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/istio_task_process_func_register.png)

其中，Config事件处理函数通过配置控制器的RegisterEventHandler方法注册，Service事件处理函数通过model.Controllcr.AppendServiceHandler方法注册。

#### Config控制器的任务处理流程
pilot/pkg/config/kube/crdclient/client.go:195

pilot/pkg/bootstrap/server.go:915

pilot/pkg/xds/discovery.go:338

#### Service控制器的任务处理流程
pilot/pkg/serviceregistry/kube/controller/controller.go:1257

pilot/pkg/bootstrap/server.go:896

pilot/pkg/serviceregistry/kube/controller/controller.go:460

pilot/pkg/serviceregistry/kube/controller/endpointcontroller.go:58,84

pilot/pkg/xds/eds.go:65,101

#### 资源更新事件处理：xDS分发
从根本上讲，Config、Service、Endpoint对资源的处理最后都是通过调用ConfigUpdate方法向XDSServer的pushChannel队列发送PushRequest实现的。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/xDS_distribute.png)

XDSServer首先通过handleUpdates线程阻塞式地接收并处理更新请求，并将PushRequest发送到XDSServer的pushQueue中，然后由sendPushes线程并发地将PushRequest发送给每一条连接的pushChannel，最后由XDSServer的流处理接口处理分发请求。

### 对xDS更新的预处理
#### 防抖动
pilot/pkg/xds/discovery.go:355,363

#### XDSServer的缓存更新
数量最大的缓存是EndpointShardsByService（全量的IstioEndpoint集合），也是在Service、Endpoint更新时，ServiceController主要维护的缓存。EnvoyXdsServer根据EndpointShardsByService可以快速构建本轮需要下发的EDS配置。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/EndpointShardsByService_upgrade.png)

EndpointShardsByService的更新主要在以下两种情况下发生:

①在Endpoint的事件处理函数中;

②在Service的事件处理函数中，主要针对Selector有变化或者Service的缓存同步晚于Endpoint的场景。

#### PushContext（推送上下文）的初始化
pilot/pkg/model/push_context.go:198,1179

#### Pilot-push事件的发送及井发控制
pilot/pkg/xds/discovery.go:482

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/pilot/pilot_push_events_and_concurrency_control.png)

### xDS配置的生成及分发
pilot/pkg/xds/ads.go:738

pilot/pkg/xds/xdsgen.go:97

Pilot主要负责6种xDS配置资源CDS、EDS、LDS、RDS、ECDS、NDS的生成及下发。以CDS生成器为例，XDSServer根据代理的属性及PushContext缓存生成原始的Cluster配置。

pilot/pkg/networking/core/v1alpha3/cluster.go:151

## Citadel
Citadel作为Istio安全的核心组件，主要用于为工作负载签发证书及处理网关的SDS请求，还能为Istiod服务器签发证书。

### 启动流程
pilot/pkg/bootstrap/server.go:230,1173

其中的关键代码包含3部分：Istio CA的创建;SDS服务器的初始化;Istio CA的启动。

#### Istio-CA的创建
pilot/pkg/bootstrap/server.go:1206

#### SDS服务器的初始化
pilot/pkg/bootstrap/server.go:525

#### Istio-CA的启动
pilot/pkg/bootstrap/server.go:1257

pilot/pkg/bootstrap/istio_ca.go:148

### CA服务器的核心原理
CA服务器在本质上是一个gRPC服务器，对外提供CreateCertificate接口，用于处理CSR请求，Istio所有工作负载证书的签发归根结底都会通过CreateCertificate接口进行。CA服务器默认基于TLS证书接收安全的gRPC连接。

![](https://raw.githubusercontent.com/wangchanggan/istio/1.18.0/docs/images/citadel/caServer_process.png)

security/pkg/server/ca/server.go:76

### 证书签发实体IstloCA
security/pkg/pki/ca/ca.go:293

security/pkg/server/ca/server.go:41

security/pkg/pki/ca/ca.go:343,421

security/pkg/pki/ra/k8s_ra.go:93,64

security/pkg/k8s/chiron/utils.go:113

### CredentialsController
pilot/pkg/credentials/model.go:21

#### 创建
pilot/pkg/credentials/kube/secrets.go:78

#### 核心原理
pilot/pkg/credentials/kube/secrets.go:285

pilot/pkg/bootstrap/server.go:536