/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RangeSpec defines the desired state of Range
type RangeSpec struct {
	CIDR       string   `json:"cidr"`
	Router     string   `json:"router"`
	SubnetMask string   `json:"subnetmask"`
	DNS        []string `json:"dns"`
}

// RangeStatus defines the observed state of Range
type RangeStatus struct {
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Range is the Schema for the ranges API
// +k8s:openapi-gen=true
type Range struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RangeSpec   `json:"spec,omitempty"`
	Status RangeStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RangeList contains a list of Range
type RangeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Range `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Range{}, &RangeList{})
}
