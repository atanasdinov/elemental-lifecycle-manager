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
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/suse/elemental-lifecycle-manager/internal/plan"
)

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

func getNodeNamesFromList(nodes []corev1.Node) []string {
	nodeNames := make([]string, 0, len(nodes))
	for _, n := range nodes {
		nodeNames = append(nodeNames, n.Name)
	}

	return nodeNames
}
