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

package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"istio.io/istio/pilot/pkg/bootstrap"
	"istio.io/istio/pilot/pkg/features"
	"istio.io/istio/pilot/pkg/serviceregistry/provider"
	"istio.io/istio/pkg/cmd"
	"istio.io/istio/pkg/config/constants"
	"istio.io/pkg/collateral"
	"istio.io/pkg/ctrlz"
	"istio.io/pkg/log"
	"istio.io/pkg/version"
)

var (
	serverArgs     *bootstrap.PilotArgs
	loggingOptions = log.DefaultOptions()
)

// NewRootCommand returns the root cobra command of pilot-discovery.
func NewRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "pilot-discovery",
		Short:        "Istio Pilot.",
		Long:         "Istio Pilot provides fleet-wide traffic management capabilities in the Istio Service Mesh.",
		SilenceUsage: true,
		FParseErrWhitelist: cobra.FParseErrWhitelist{
			// Allow unknown flags for backward-compatibility.
			UnknownFlags: true,
		},
		PreRunE: func(c *cobra.Command, args []string) error {
			cmd.AddFlags(c)
			return nil
		},
	}

	discoveryCmd := newDiscoveryCommand()
	addFlags(discoveryCmd)
	rootCmd.AddCommand(discoveryCmd)
	rootCmd.AddCommand(version.CobraCommand())
	rootCmd.AddCommand(collateral.CobraCommand(rootCmd, &doc.GenManHeader{
		Title:   "Istio Pilot Discovery",
		Section: "pilot-discovery CLI",
		Manual:  "Istio Pilot Discovery",
	}))
	rootCmd.AddCommand(requestCmd)

	return rootCmd
}

func newDiscoveryCommand() *cobra.Command {
	// pilot-discovery的启动命令
	return &cobra.Command{
		Use:   "discovery",
		Short: "Start Istio proxy discovery service.",
		Args:  cobra.ExactArgs(0),
		FParseErrWhitelist: cobra.FParseErrWhitelist{
			// Allow unknown flags for backward-compatibility.
			UnknownFlags: true,
		},
		PreRunE: func(c *cobra.Command, args []string) error {
			// 设置日志系统，主要设置日志级别，输出路径等;
			if err := log.Configure(loggingOptions); err != nil {
				return err
			}
			// 校验启动参数，防止传入非法参数。
			if err := validateFlags(serverArgs); err != nil {
				return err
			}
			if err := serverArgs.Complete(); err != nil {
				return err
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			cmd.PrintFlags(c.Flags())

			// Create the stop channel for all the servers.
			stop := make(chan struct{})

			// Create the server for the discovery service.
			// 创建Pilot服务器对象
			// Pilot Server对象是注册中心与Sidecar代理之间的桥梁，它将服务及配置资源转化成xDS配置，再通过gRPC连接流将xDS配置发送给Sidecar。
			discoveryServer, err := bootstrap.NewServer(serverArgs)
			if err != nil {
				return fmt.Errorf("failed to create discovery service: %v", err)
			}

			// Start the server
			// 启动Pilot服务器
			// Pilot服务器的启动是通过执行其所有模块的启动函数startFuncs实现的
			// 在模块初始化时都会通过func (s *Server) addStartFunc(fn server.Component)接口将自己的启动任务注册到服务器对象的server属性中
			if err := discoveryServer.Start(stop); err != nil {
				return fmt.Errorf("failed to start discovery service: %v", err)
			}

			cmd.WaitSignal(stop)
			// Wait until we shut down. In theory this could block forever; in practice we will get
			// forcibly shut down after 30s in Kubernetes.
			// 优雅退出
			discoveryServer.WaitUntilCompletion()
			return nil
		},
	}
}

func addFlags(c *cobra.Command) {
	serverArgs = bootstrap.NewPilotArgs(func(p *bootstrap.PilotArgs) {
		// Set Defaults
		p.CtrlZOptions = ctrlz.DefaultOptions()
		// TODO replace with mesh config?
		p.InjectionOptions = bootstrap.InjectionOptions{
			InjectionDirectory: "./var/lib/istio/inject",
		}
	})

	// Process commandline args.
	c.PersistentFlags().StringSliceVar(&serverArgs.RegistryOptions.Registries, "registries",
		[]string{string(provider.Kubernetes)},
		fmt.Sprintf("Comma separated list of platform service registries to read from (choose one or more from {%s, %s})",
			provider.Kubernetes, provider.Mock))
	c.PersistentFlags().StringVar(&serverArgs.RegistryOptions.ClusterRegistriesNamespace, "clusterRegistriesNamespace",
		serverArgs.RegistryOptions.ClusterRegistriesNamespace, "Namespace for ConfigMap which stores clusters configs")
	c.PersistentFlags().StringVar(&serverArgs.RegistryOptions.KubeConfig, "kubeconfig", "",
		"Use a Kubernetes configuration file instead of in-cluster configuration")
	c.PersistentFlags().StringVar(&serverArgs.MeshConfigFile, "meshConfig", "./etc/istio/config/mesh",
		"File name for Istio mesh configuration. If not specified, a default mesh will be used.")
	c.PersistentFlags().StringVar(&serverArgs.NetworksConfigFile, "networksConfig", "./etc/istio/config/meshNetworks",
		"File name for Istio mesh networks configuration. If not specified, a default mesh networks will be used.")
	c.PersistentFlags().StringVarP(&serverArgs.Namespace, "namespace", "n", bootstrap.PodNamespace,
		"Select a namespace where the controller resides. If not set, uses ${POD_NAMESPACE} environment variable")
	c.PersistentFlags().DurationVar(&serverArgs.ShutdownDuration, "shutdownDuration", 10*time.Second,
		"Duration the discovery server needs to terminate gracefully")

	// RegistryOptions Controller options
	c.PersistentFlags().StringVar(&serverArgs.RegistryOptions.FileDir, "configDir", "",
		"Directory to watch for updates to config yaml files. If specified, the files will be used as the source of config, rather than a CRD client.")
	c.PersistentFlags().StringVar(&serverArgs.RegistryOptions.KubeOptions.DomainSuffix, "domain", constants.DefaultClusterLocalDomain,
		"DNS domain suffix")
	c.PersistentFlags().StringVar((*string)(&serverArgs.RegistryOptions.KubeOptions.ClusterID), "clusterID", features.ClusterName,
		"The ID of the cluster that this Istiod instance resides")
	c.PersistentFlags().StringToStringVar(&serverArgs.RegistryOptions.KubeOptions.ClusterAliases, "clusterAliases", map[string]string{},
		"Alias names for clusters")

	// using address, so it can be configured as localhost:.. (possibly UDS in future)
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.HTTPAddr, "httpAddr", ":8080",
		"Discovery service HTTP address")
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.HTTPSAddr, "httpsAddr", ":15017",
		"Injection and validation service HTTPS address")
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.GRPCAddr, "grpcAddr", ":15010",
		"Discovery service gRPC address")
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.SecureGRPCAddr, "secureGRPCAddr", ":15012",
		"Discovery service secured gRPC address")
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.MonitoringAddr, "monitoringAddr", ":15014",
		"HTTP address to use for pilot's self-monitoring information")
	c.PersistentFlags().BoolVar(&serverArgs.ServerOptions.EnableProfiling, "profile", true,
		"Enable profiling via web interface host:port/debug/pprof")

	// Use TLS certificates if provided.
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.TLSOptions.CaCertFile, "caCertFile", "",
		"File containing the x509 Server CA Certificate")
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.TLSOptions.CertFile, "tlsCertFile", "",
		"File containing the x509 Server Certificate")
	c.PersistentFlags().StringVar(&serverArgs.ServerOptions.TLSOptions.KeyFile, "tlsKeyFile", "",
		"File containing the x509 private key matching --tlsCertFile")
	c.PersistentFlags().StringSliceVar(&serverArgs.ServerOptions.TLSOptions.TLSCipherSuites, "tls-cipher-suites", nil,
		"Comma-separated list of cipher suites for istiod TLS server. "+
			"If omitted, the default Go cipher suites will be used. \n"+
			"Preferred values: "+strings.Join(secureTLSCipherNames(), ", ")+". \n"+
			"Insecure values: "+strings.Join(insecureTLSCipherNames(), ", ")+".")

	c.PersistentFlags().Float32Var(&serverArgs.RegistryOptions.KubeOptions.KubernetesAPIQPS, "kubernetesApiQPS", 80.0,
		"Maximum QPS when communicating with the kubernetes API")

	c.PersistentFlags().IntVar(&serverArgs.RegistryOptions.KubeOptions.KubernetesAPIBurst, "kubernetesApiBurst", 160,
		"Maximum burst for throttle when communicating with the kubernetes API")

	// Attach the Istio logging options to the command.
	loggingOptions.AttachCobraFlags(c)

	// Attach the Istio Ctrlz options to the command.
	serverArgs.CtrlZOptions.AttachCobraFlags(c)

	// Attach the Istio Keepalive options to the command.
	serverArgs.KeepaliveOptions.AttachCobraFlags(c)
}
