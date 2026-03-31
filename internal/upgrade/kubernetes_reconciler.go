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
	"context"
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	helmv1 "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	upgradecattlev1 "github.com/rancher/system-upgrade-controller/pkg/apis/upgrade.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
	"github.com/suse/elemental-lifecycle-manager/internal/helm"
	"github.com/suse/elemental-lifecycle-manager/internal/plan"
)

// KubernetesReconciler reconciles Kubernetes upgrades via SUC Plans and verifies node state.
type KubernetesReconciler struct {
	client.Client
	sucReconciler PlanReconciler
	helmClient    helm.Client
}

func NewKubernetesReconciler(c client.Client, h helm.Client, sucReconciler PlanReconciler) *KubernetesReconciler {
	if sucReconciler == nil {
		sucReconciler = &planReconciler{Client: c}
	}

	return &KubernetesReconciler{Client: c, sucReconciler: sucReconciler, helmClient: h}
}

func (r *KubernetesReconciler) Phase() Phase {
	return PhaseKubernetes
}

// TODO: Enable K8s reconciliation once K8s delivery and upgrade mechanism are finalized.
func (r *KubernetesReconciler) ShouldReconcile(config *Config) bool {
	return false
	// return config.Kubernetes != nil
}

// TODO: Rework K8s reconcile logic to match OS reconcile logic.
// To be done once K8s delivery and upgrade mechanism are finalized.
func (r *KubernetesReconciler) Reconcile(ctx context.Context, config *Config) (*PhaseStatus, error) {
	logger := log.FromContext(ctx)
	k8sConfig := config.Kubernetes

	logger.Info("Reconciling Kubernetes upgrade",
		"image", k8sConfig.Image,
		"version", k8sConfig.Version,
		"release", config.ReleaseName)

	plans, err := r.preparePlans(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("preparing Kubernetes upgrade plans: %w", err)
	}

	for _, p := range plans {
		result, err := r.sucReconciler.Reconcile(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("reconciling Kubernetes upgrade plan '%s': %w", p.Name, err)
		}

		if result.Status.State != lifecyclev1alpha1.PlanComplete {
			return result.Status, nil
		}

		if !allNodesAtKubernetesVersion(result.Nodes, k8sConfig.Version) {
			return &PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("Plan %s completed, waiting for node upgrade verification", p.Name),
			}, nil
		}
	}

	if status, err := r.checkCoreComponents(ctx, k8sConfig.CoreComponents); err != nil {
		return nil, err
	} else if status != nil {
		return status, nil
	}

	return &PhaseStatus{
		State:   lifecyclev1alpha1.UpgradeSucceeded,
		Message: "All nodes upgraded successfully",
	}, nil
}

func (r *KubernetesReconciler) preparePlans(ctx context.Context, config *Config) (plans []*upgradecattlev1.Plan, err error) {
	k8sConfig := config.Kubernetes
	cpPlan := plan.KubernetesControlPlane(config.ReleaseName, k8sConfig.Image, k8sConfig.Version, k8sConfig.DrainOpts.ControlPlane)
	planList := []*upgradecattlev1.Plan{cpPlan}

	allNodes := &corev1.NodeList{}
	if err := r.List(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("listing cluster nodes: %w", err)
	}

	if !isControlPlaneOnlyCluster(allNodes.Items) {
		wkPlan := plan.KubernetesWorker(config.ReleaseName, k8sConfig.Image, k8sConfig.Version, k8sConfig.DrainOpts.Worker)
		planList = append(planList, wkPlan)
	}

	return planList, nil
}

// checkCoreComponents verifies that all Kubernetes core components are upgraded.
// Returns nil status if all components are upgraded or if no components are configured.
// Returns an in-progress status if any component is still upgrading.
func (r *KubernetesReconciler) checkCoreComponents(ctx context.Context, components []CoreComponent) (*PhaseStatus, error) {
	if len(components) == 0 {
		return nil, nil
	}

	logger := log.FromContext(ctx)

	for _, component := range components {
		upgraded, err := r.isCoreComponentUpgraded(ctx, &component)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("Core component not found, skipping", "component", component.Name)
				continue
			}
			return nil, fmt.Errorf("checking core component %s: %w", component.Name, err)
		}

		if !upgraded {
			logger.Info("Waiting for core component to be upgraded", "component", component.Name)
			return &PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("Waiting for %s core component to be upgraded", component.Name),
			}, nil
		}
	}

	return nil, nil
}

// isCoreComponentUpgraded checks if a single core component has been upgraded.
func (r *KubernetesReconciler) isCoreComponentUpgraded(ctx context.Context, component *CoreComponent) (bool, error) {
	switch component.Type {
	case CoreComponentHelmChart:
		return r.isHelmChartComponentUpgraded(ctx, component)
	case CoreComponentDeployment:
		return r.isDeploymentComponentUpgraded(ctx, component)
	default:
		return false, fmt.Errorf("unsupported component type: %s", component.Type)
	}
}

// isHelmChartComponentUpgraded checks if a HelmChart component has been upgraded.
func (r *KubernetesReconciler) isHelmChartComponentUpgraded(ctx context.Context, component *CoreComponent) (bool, error) {
	chart := &helmv1.HelmChart{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      component.Name,
		Namespace: "kube-system",
	}, chart); err != nil {
		return false, err
	}

	// Check if the upgrade job exists and is complete
	if chart.Status.JobName == "" {
		return false, nil
	}

	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      chart.Status.JobName,
		Namespace: chart.Namespace,
	}, job); err != nil {
		// Job might be cleaned up after completion, check actual helm release version
		if apierrors.IsNotFound(err) {
			return r.helmReleaseVersionMatches(component.Name, component.Version)
		}
		return false, err
	}

	// Check if job is complete
	isComplete := slices.ContainsFunc(job.Status.Conditions, func(c batchv1.JobCondition) bool {
		return c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue
	})

	if !isComplete {
		return false, nil
	}

	// Job is complete, verify the actual helm release has the expected version
	return r.helmReleaseVersionMatches(component.Name, component.Version)
}

// helmReleaseVersionMatches retrieves the actual Helm release and checks if its version matches.
func (r *KubernetesReconciler) helmReleaseVersionMatches(releaseName, expectedVersion string) (bool, error) {
	release, err := r.helmClient.RetrieveRelease(releaseName)
	if err != nil {
		return false, fmt.Errorf("retrieving helm release %s: %w", releaseName, err)
	}

	return release.ChartVersion == expectedVersion, nil
}

// isDeploymentComponentUpgraded checks if a Deployment component has been upgraded.
func (r *KubernetesReconciler) isDeploymentComponentUpgraded(ctx context.Context, component *CoreComponent) (bool, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      component.Name,
		Namespace: "kube-system",
	}, deployment); err != nil {
		return false, err
	}

	// Check if deployment is available
	isAvailable := slices.ContainsFunc(deployment.Status.Conditions, func(c appsv1.DeploymentCondition) bool {
		return c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue
	})

	if !isAvailable {
		return false, nil
	}

	// If no container images specified, availability is sufficient
	if len(component.Containers) == 0 {
		return true, nil
	}

	// Verify container images match expected versions.
	// Use non-strict mode to allow for different registries (e.g., private registry).
	return containsContainerImages(deployment.Spec.Template.Spec.Containers, component.Containers, false), nil
}

// containsContainerImages validates that a given map of "container: image" references
// exist in a slice of corev1.Containers.
//
// Returns 'true' only if all the "container: image" references from the 'contains' map
// are present in the corev1.Containers slice.
//
// If 'strict' is true, will require for the corev1.Container.Image to be exactly the same
// as the image string defined in the 'contains' map.
//
// If 'strict' is false, will require for the corev1.Container.Image to contain the image string defined
// in the 'contains' map. Useful for use-cases where the image registry may change
// based on the environment (e.g. private registry).
func containsContainerImages(containers []corev1.Container, contains map[string]string, strict bool) bool {
	foundContainers := 0
	for _, container := range containers {
		image, ok := contains[container.Name]
		if !ok {
			// Skip containers that are not in the 'contains' map.
			continue
		}
		foundContainers++

		if strict && container.Image != image {
			return false
		}

		if !strict && !strings.Contains(container.Image, image) {
			return false
		}
	}

	return foundContainers == len(contains)
}
