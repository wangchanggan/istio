# Istio源码分析
Source Code From

https://github.com/istio/istio/archive/refs/tags/1.18.0.zip

## 目录
-   [Istio源码分析](#istio源码分析)
    -   [目录](#目录)
    -   [Pilot](#pilot)
        -   [启动流程](#启动流程)

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

![](/docs/images/configController_init.png)

pilot/pkg/bootstrap/configcontroller.go:61,135,346

pilot/pkg/config/kube/crdclient/client.go:61

另外，虽然Istio没有适配器可直接对接其他注册中心，但Istio提供了可扩展的接口协议MCP，方便用户集成其他第三方注册中心。MCP ConfigController与Kubernetes ConfigController基本类似，均实现了ConfigStoreController接口，支持Istio配置资源的发现，并提供了缓存管理功能。在创建MCP ConfigController时需要通过MeshConfig.ConfigSources指定MCP服务器的地址。

![](/docs/images/MCP_configController_init.png)

pilot/pkg/bootstrap/configcontroller.go:211
#### 核心工作机制
![](/docs/images/CRD_operator_process.png)
pilot/pkg/config/kube/crdclient/cache_handler.go:86,40,77

pilot/pkg/bootstrap/server.go:903

完整的Config事件处理流程:

（1）EventHandler构造任务(Task)，任务实际上是对onEvent的封装。

（2）EventHandler将任务推送到ConfigController的任务队列(Task queue)。

（3）任务处理协程阻塞式地读取任务队列，执行任务，通过onEvent方法处理事件，并通过configHandler触发xDS的更新。

![](/docs/images/config_event_handling.png)
