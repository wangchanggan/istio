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