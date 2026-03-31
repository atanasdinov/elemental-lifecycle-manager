/*
Copyright 2026.

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

package upgrade

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/suse/elemental-lifecycle-manager/internal/plan"
)

// filterNodesBySelector returns nodes matching the given label selector.
func filterNodesBySelector(nodes []corev1.Node, nodeSelector *metav1.LabelSelector) ([]corev1.Node, error) {
	if nodeSelector == nil {
		return nodes, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(nodeSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing node selector: %w", err)
	}

	var matching []corev1.Node
	for _, node := range nodes {
		if selector.Matches(labels.Set(node.Labels)) {
			matching = append(matching, node)
		}
	}

	return matching, nil
}

// isNodeReady returns true if the node has a Ready condition with status True.
func isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// isControlPlaneOnlyCluster returns true if all nodes in the cluster are control plane nodes.
func isControlPlaneOnlyCluster(nodes []corev1.Node) bool {
	for _, node := range nodes {
		if _, isControlPlane := node.Labels[plan.ControlPlaneLabel]; !isControlPlane {
			return false
		}
	}
	return true
}

// allNodesReady returns true if all the specified nodes are
// schedulable and in a 'Ready' status.
func allNodesReady(nodes []corev1.Node) bool {
	if len(nodes) == 0 {
		return false
	}

	for _, node := range nodes {
		if !isNodeReady(&node) {
			return false
		}

		if node.Spec.Unschedulable {
			return false
		}
	}

	return true
}

// allNodesAtKubernetesVersion returns true if all nodes have the target Kubernetes version.
// Returns false if no nodes are provided.
// A node is considered upgraded when:
// - It is in Ready condition
// - It is not marked as unschedulable
// - Its kubelet version matches the target version
func allNodesAtKubernetesVersion(nodes []corev1.Node, targetVersion string) bool {
	if len(nodes) == 0 {
		return false
	}

	for _, node := range nodes {
		if !isNodeReady(&node) {
			return false
		}

		if node.Spec.Unschedulable {
			return false
		}

		if !kubeletVersionMatches(node.Status.NodeInfo.KubeletVersion, targetVersion) {
			return false
		}
	}

	return true
}

// kubeletVersionMatches checks if the kubelet version matches the target version.
// Handles version format differences (e.g., "v1.30.0" vs "1.30.0").
func kubeletVersionMatches(kubeletVersion, targetVersion string) bool {
	// Normalize both versions by removing 'v' prefix if present
	kubelet := strings.TrimPrefix(kubeletVersion, "v")
	target := strings.TrimPrefix(targetVersion, "v")

	return kubelet == target
}
