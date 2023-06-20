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

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	multierror "github.com/hashicorp/go-multierror"
	admissionv1 "k8s.io/api/admission/v1"
	kubeApiAdmissionv1beta1 "k8s.io/api/admission/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"istio.io/istio/pilot/pkg/config/kube/crd"
	"istio.io/istio/pkg/config/schema/collection"
	"istio.io/istio/pkg/config/schema/resource"
	"istio.io/istio/pkg/config/validation"
	"istio.io/istio/pkg/kube"
	"istio.io/pkg/log"
)

var scope = log.RegisterScope("validationServer", "validation webhook server", 0)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()

	// Expect AdmissionRequest to only include these top-level field names
	validFields = map[string]bool{
		"apiVersion": true,
		"kind":       true,
		"metadata":   true,
		"spec":       true,
		"status":     true,
	}
)

func init() {
	_ = admissionv1.AddToScheme(runtimeScheme)
	_ = kubeApiAdmissionv1beta1.AddToScheme(runtimeScheme)
}

// Options contains the configuration for the Istio Pilot validation
// admission controller.
type Options struct {
	// Schemas provides a description of all configuration resources.
	Schemas collection.Schemas

	// DomainSuffix is the DNS domain suffix for Pilot CRD resources,
	// e.g. cluster.local.
	DomainSuffix string

	// Port where the webhook is served. the number should be greater than 1024 for non-root
	// user, because non-root user cannot bind port number less than 1024
	// Mainly used for testing. Webhook server is started by Istiod.
	Port uint

	// Use an existing mux instead of creating our own.
	Mux *http.ServeMux
}

// String produces a stringified version of the arguments for debugging.
func (o Options) String() string {
	buf := &bytes.Buffer{}

	_, _ = fmt.Fprintf(buf, "DomainSuffix: %s\n", o.DomainSuffix)
	_, _ = fmt.Fprintf(buf, "Port: %d\n", o.Port)

	return buf.String()
}

// DefaultArgs allocates an Options struct initialized with Webhook's default configuration.
func DefaultArgs() Options {
	return Options{
		Port: 9443,
	}
}

// Webhook implements the validating admission webhook for validating Istio configuration.
type Webhook struct {
	// pilot
	schemas      collection.Schemas
	domainSuffix string
}

// New creates a new instance of the admission webhook server.
func New(o Options) (*Webhook, error) {
	if o.Mux == nil {
		scope.Error("mux not set correctly")
		return nil, errors.New("expected mux to be passed, but was not passed")
	}
	wh := &Webhook{
		schemas:      o.Schemas,
		domainSuffix: o.DomainSuffix,
	}

	// 初始化配置校验
	// Webhook Server在初始化时会注册API配置校验handler(serveValidate)
	o.Mux.HandleFunc("/validate", wh.serveValidate)
	o.Mux.HandleFunc("/validate/", wh.serveValidate)

	return wh, nil
}

func toAdmissionResponse(err error) *kube.AdmissionResponse {
	return &kube.AdmissionResponse{Result: &metav1.Status{Message: err.Error()}}
}

type admitFunc func(*kube.AdmissionRequest) *kube.AdmissionResponse

func serve(w http.ResponseWriter, r *http.Request, admit admitFunc) {
	var body []byte
	if r.Body != nil {
		// 读取请求体，为了内存安全，并没有直接使用ioutil.ReadAll
		// 而是通过io.LimitedReader限制读取数据的大小
		if data, err := kube.HTTPConfigReader(r); err == nil {
			body = data
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if len(body) == 0 {
		reportValidationHTTPError(http.StatusBadRequest)
		http.Error(w, "no body found", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		reportValidationHTTPError(http.StatusUnsupportedMediaType)
		http.Error(w, "invalid Content-Type, want `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var reviewResponse *kube.AdmissionResponse
	var obj runtime.Object
	var ar *kube.AdmissionReview
	// 解码请求数据
	if out, _, err := deserializer.Decode(body, nil, obj); err != nil {
		reviewResponse = toAdmissionResponse(fmt.Errorf("could not decode body: %v", err))
	} else {
		// 实现了一个Admission请求版本的转换器，支持v1beta1和v1两种版本的Admission API
		ar, err = kube.AdmissionReviewKubeToAdapter(out)
		if err != nil {
			reviewResponse = toAdmissionResponse(fmt.Errorf("could not decode object: %v", err))
		} else {
			// 校验请求
			reviewResponse = admit(ar.Request)
		}
	}

	response := kube.AdmissionReview{}
	response.Response = reviewResponse
	var responseKube runtime.Object
	var apiVersion string
	if ar != nil {
		apiVersion = ar.APIVersion
		response.TypeMeta = ar.TypeMeta
		if response.Response != nil {
			if ar.Request != nil {
				response.Response.UID = ar.Request.UID
			}
		}
	}
	// 实现了一个Admission响应版本的转换器，支持v1beta1和v1两种版本的Admission API
	responseKube = kube.AdmissionReviewAdapterToKube(&response, apiVersion)
	resp, err := json.Marshal(responseKube)
	if err != nil {
		reportValidationHTTPError(http.StatusInternalServerError)
		http.Error(w, fmt.Sprintf("could encode response: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(resp); err != nil {
		reportValidationHTTPError(http.StatusInternalServerError)
		http.Error(w, fmt.Sprintf("could write response: %v", err), http.StatusInternalServerError)
	}
}

// // WebhookServer在启动后开始接收来自客户端的请求并对配置进行校验，最后返回校验结果，它主要通过seveValidate调用serve方法来实现对Istio API配置的校验。
func (wh *Webhook) serveValidate(w http.ResponseWriter, r *http.Request) {
	serve(w, r, wh.validate)
}

func (wh *Webhook) validate(request *kube.AdmissionRequest) *kube.AdmissionResponse {
	isDryRun := request.DryRun != nil && *request.DryRun
	addDryRunMessageIfNeeded := func(errStr string) error {
		err := fmt.Errorf("%s", errStr)
		if isDryRun {
			err = fmt.Errorf("%s (dry run)", err)
		}
		return err
	}
	switch request.Operation {
	// 检查请求类型，只处理Create、Update两种类型的请求
	case kube.Create, kube.Update:
	default:
		scope.Warnf("Unsupported webhook operation %v", addDryRunMessageIfNeeded(request.Operation))
		reportValidationFailed(request, reasonUnsupportedOperation, isDryRun)
		return &kube.AdmissionResponse{Allowed: true}
	}

	// 将数据进行格式转换、校验。
	// 在Istio Schema中保存了Istio所有的API配置信息及校验方法。
	// 通过数据对象的类型即可在Schema中找到相应的校验方法，对数据进行校验
	var obj crd.IstioKind
	if err := json.Unmarshal(request.Object.Raw, &obj); err != nil {
		scope.Infof("cannot decode configuration: %v", addDryRunMessageIfNeeded(err.Error()))
		reportValidationFailed(request, reasonYamlDecodeError, isDryRun)
		return toAdmissionResponse(fmt.Errorf("cannot decode configuration: %v", err))
	}

	gvk := obj.GroupVersionKind()

	s, exists := wh.schemas.FindByGroupVersionAliasesKind(resource.FromKubernetesGVK(&gvk))
	if !exists {
		scope.Infof("unrecognized type %v", addDryRunMessageIfNeeded(obj.GroupVersionKind().String()))
		reportValidationFailed(request, reasonUnknownType, isDryRun)
		return toAdmissionResponse(fmt.Errorf("unrecognized type %v", obj.GroupVersionKind()))
	}

	// 将Kubernetes对象转换成Istio API对象
	out, err := crd.ConvertObject(s, &obj, wh.domainSuffix)
	if err != nil {
		scope.Infof("error decoding configuration: %v", addDryRunMessageIfNeeded(err.Error()))
		reportValidationFailed(request, reasonCRDConversionError, isDryRun)
		return toAdmissionResponse(fmt.Errorf("error decoding configuration: %v", err))
	}

	// 校验Istio API对象
	// 每种API对象的ValidateConfig都在pkg/config/schema/collections/collections.gen.go中注册，而且各不相同。
	warnings, err := s.ValidateConfig(*out)
	if err != nil {
		scope.Infof("configuration is invalid: %v", addDryRunMessageIfNeeded(err.Error()))
		reportValidationFailed(request, reasonInvalidConfig, isDryRun)
		return toAdmissionResponse(fmt.Errorf("configuration is invalid: %v", err))
	}

	if reason, err := checkFields(request.Object.Raw, request.Kind.Kind, request.Namespace, obj.Name); err != nil {
		reportValidationFailed(request, reason, isDryRun)
		return toAdmissionResponse(err)
	}

	reportValidationPass(request)
	return &kube.AdmissionResponse{Allowed: true, Warnings: toKubeWarnings(warnings)}
}

func toKubeWarnings(warn validation.Warning) []string {
	if warn == nil {
		return nil
	}
	me, ok := warn.(*multierror.Error)
	if ok {
		res := []string{}
		for _, e := range me.Errors {
			res = append(res, e.Error())
		}
		return res
	}
	return []string{warn.Error()}
}

func checkFields(raw []byte, kind string, namespace string, name string) (string, error) {
	trial := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &trial); err != nil {
		scope.Infof("cannot decode configuration fields: %v", err)
		return reasonYamlDecodeError, fmt.Errorf("cannot decode configuration fields: %v", err)
	}

	for key := range trial {
		if _, ok := validFields[key]; !ok {
			scope.Infof("unknown field %q on %s resource %s/%s",
				key, kind, namespace, name)
			return reasonInvalidConfig, fmt.Errorf("unknown field %q on %s resource %s/%s",
				key, kind, namespace, name)
		}
	}

	return "", nil
}

// validatePort checks that the network port is in range
func validatePort(port int) error {
	if 1 <= port && port <= 65535 {
		return nil
	}
	return fmt.Errorf("port number %d must be in the range 1..65535", port)
}

// Validate tests if the Options has valid params.
func (o Options) Validate() error {
	var errs *multierror.Error
	if err := validatePort(int(o.Port)); err != nil {
		errs = multierror.Append(errs, err)
	}
	return errs.ErrorOrNil()
}
