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
	"reflect"
	"testing"

	networking "istio.io/api/networking/v1alpha3"
	"istio.io/istio/pkg/config"
)

// TestEnvoyFilterMatch tests the matching logic for EnvoyFilter, in particular the regex -> prefix optimization
func TestEnvoyFilterMatch(t *testing.T) {
	cases := []struct {
		name                  string
		config                *networking.EnvoyFilter
		expectedVersionPrefix string
		matches               map[string]bool
	}{
		{
			"version prefix match",
			&networking.EnvoyFilter{
				ConfigPatches: []*networking.EnvoyFilter_EnvoyConfigObjectPatch{
					{
						Patch: &networking.EnvoyFilter_Patch{},
						Match: &networking.EnvoyFilter_EnvoyConfigObjectMatch{
							Proxy: &networking.EnvoyFilter_ProxyMatch{ProxyVersion: `^1\.6.*`},
						},
					},
				},
			},
			"1.6",
			map[string]bool{
				"1.6":         true,
				"1.6.0":       true,
				"1.6-dev.foo": true,
				"1.5":         false,
				"11.6":        false,
				"foo1.6":      false,
			},
		},
		{
			"version prefix mismatch",
			&networking.EnvoyFilter{
				ConfigPatches: []*networking.EnvoyFilter_EnvoyConfigObjectPatch{
					{
						Patch: &networking.EnvoyFilter_Patch{},
						Match: &networking.EnvoyFilter_EnvoyConfigObjectMatch{
							Proxy: &networking.EnvoyFilter_ProxyMatch{ProxyVersion: `1\.6.*`},
						},
					},
				},
			},
			"",
			map[string]bool{
				"1.6":         true,
				"1.6.0":       true,
				"1.6-dev.foo": true,
				"1.5":         false,
				"11.6":        true,
				"foo1.6":      true,
			},
		},
		{
			"non-numeric",
			&networking.EnvoyFilter{
				ConfigPatches: []*networking.EnvoyFilter_EnvoyConfigObjectPatch{
					{
						Patch: &networking.EnvoyFilter_Patch{},
						Match: &networking.EnvoyFilter_EnvoyConfigObjectMatch{
							Proxy: &networking.EnvoyFilter_ProxyMatch{ProxyVersion: `foobar`},
						},
					},
				},
			},
			"",
			map[string]bool{
				"1.6":         false,
				"1.6.0":       false,
				"1.6-dev.foo": false,
				"foobar":      true,
			},
		},
	}
	for _, tt := range cases {
		got := convertToEnvoyFilterWrapper(&config.Config{
			Meta: config.Meta{},
			Spec: tt.config,
		})
		if len(got.Patches[networking.EnvoyFilter_INVALID]) != 1 {
			t.Fatalf("unexpected patches: %v", got.Patches)
		}
		filter := got.Patches[networking.EnvoyFilter_INVALID][0]
		if filter.ProxyPrefixMatch != tt.expectedVersionPrefix {
			t.Errorf("unexpected prefix: got %v wanted %v", filter.ProxyPrefixMatch, tt.expectedVersionPrefix)
		}
		for ver, match := range tt.matches {
			got := proxyMatch(&Proxy{Metadata: &NodeMetadata{IstioVersion: ver}}, filter)
			if got != match {
				t.Errorf("expected %v to match %v, got %v", ver, match, got)
			}
		}
	}
}

func TestConvertEnvoyFilter(t *testing.T) {
	cfilter := convertToEnvoyFilterWrapper(&config.Config{
		Meta: config.Meta{Name: "test", Namespace: "testns"},
		Spec: &networking.EnvoyFilter{
			ConfigPatches: []*networking.EnvoyFilter_EnvoyConfigObjectPatch{
				{
					Patch: &networking.EnvoyFilter_Patch{},
					Match: &networking.EnvoyFilter_EnvoyConfigObjectMatch{
						Proxy: &networking.EnvoyFilter_ProxyMatch{ProxyVersion: `foobar`},
					},
				},
			},
		},
	})
	if cfilter.Name != "test" && cfilter.Namespace != "testns" {
		t.Errorf("expected name %s got %s and namespace %s got %s", "test", cfilter.Name, "testns", cfilter.Namespace)
	}
}

func TestKeysApplyingTo(t *testing.T) {
	e := &EnvoyFilterWrapper{
		Patches: map[networking.EnvoyFilter_ApplyTo][]*EnvoyFilterConfigPatchWrapper{
			networking.EnvoyFilter_HTTP_FILTER: {
				{
					Name:      "http",
					Namespace: "ns",
					FullName:  genFullName("ns", "http"),
				},
			},
			networking.EnvoyFilter_NETWORK_FILTER: {
				{
					Name:      "b",
					Namespace: "ns",
					FullName:  genFullName("ns", "b"),
				},
				{
					Name:      "c",
					Namespace: "ns",
					FullName:  genFullName("ns", "c"),
				},
				{
					Name:      "a",
					Namespace: "ns",
					FullName:  genFullName("ns", "a"),
				},
				{
					Name:      "a",
					Namespace: "ns",
					FullName:  genFullName("ns", "a"),
				},
			},
		},
	}
	tests := []struct {
		name    string
		efw     *EnvoyFilterWrapper
		applyTo []networking.EnvoyFilter_ApplyTo
		want    []string
	}{
		{
			name:    "http filters",
			efw:     e,
			applyTo: []networking.EnvoyFilter_ApplyTo{networking.EnvoyFilter_HTTP_FILTER},
			want:    []string{"ns/http"},
		},
		{
			name:    "network filters",
			efw:     e,
			applyTo: []networking.EnvoyFilter_ApplyTo{networking.EnvoyFilter_NETWORK_FILTER},
			want:    []string{"ns/a", "ns/b", "ns/c"},
		},
		{
			name:    "cluster filters",
			efw:     e,
			applyTo: []networking.EnvoyFilter_ApplyTo{networking.EnvoyFilter_CLUSTER},
			want:    []string{},
		},
		{
			name: "route filters",
			efw:  e,
			applyTo: []networking.EnvoyFilter_ApplyTo{
				networking.EnvoyFilter_ROUTE_CONFIGURATION,
				networking.EnvoyFilter_VIRTUAL_HOST,
				networking.EnvoyFilter_HTTP_ROUTE,
			},
			want: []string{},
		},
		{
			name: "http and network filters",
			efw:  e,
			applyTo: []networking.EnvoyFilter_ApplyTo{
				networking.EnvoyFilter_HTTP_FILTER,
				networking.EnvoyFilter_NETWORK_FILTER,
			},
			want: []string{"ns/a", "ns/b", "ns/c", "ns/http"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.efw.KeysApplyingTo(tt.applyTo...); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
