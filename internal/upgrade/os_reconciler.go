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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	upgradecattlev1 "github.com/rancher/system-upgrade-controller/pkg/apis/upgrade.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
	"github.com/suse/elemental-lifecycle-manager/internal/plan"
)

// OSReconciler reconciles OS upgrades via SUC Plans and verifies node state.
type OSReconciler struct {
	client.Client
	sucReconciler PlanReconciler
}

func NewOSReconciler(c client.Client, sucReconciler PlanReconciler) *OSReconciler {
	return &OSReconciler{Client: c, sucReconciler: sucReconciler}
}

func (r *OSReconciler) Phase() Phase {
	return PhaseOS
}

func (r *OSReconciler) Reconcile(ctx context.Context, config *Config) (*PhaseStatus, error) {
	if config == nil || config.OS == nil {
		return r.Phase().SkippedStatus(), nil
	}

	logger := log.FromContext(ctx)
	osConfig := config.OS
	logger.Info("Reconciling OS upgrade",
		"image", osConfig.Image,
		"version", osConfig.Version,
		"release", config.ReleaseNamespacedName.Name)

	plans, err := r.preparePlans(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("preparing OS upgrade plans: %w", err)
	}

	for _, p := range plans {
		logger.Info("Reconciling OS upgrade plan",
			"plan", p.Name,
			"namespace", p.Namespace,
		)
		result, err := r.sucReconciler.Reconcile(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("reconciling OS upgrade plan '%s': %w", p.Name, err)
		}

		if result.Status.State != lifecyclev1alpha1.PlanComplete {
			return result.Status, nil
		}

		if !allNodesReady(result.Nodes) {
			return &PhaseStatus{
				State:   lifecyclev1alpha1.UpgradeInProgress,
				Message: fmt.Sprintf("Plan %s completed, waiting for node upgrade verification", p.Name),
			}, nil
		}

		logger.Info("OS upgrade plan completed",
			"plan", p.Name,
			"namespace", p.Namespace,
			"applied_on", getNodeNamesFromList(result.Nodes),
		)
	}

	return &PhaseStatus{
		State:   lifecyclev1alpha1.UpgradeSucceeded,
		Message: "All nodes upgraded successfully",
	}, nil
}

func (r *OSReconciler) preparePlans(ctx context.Context, config *Config) (plans []*upgradecattlev1.Plan, err error) {
	osConfig := config.OS
	cpPlan, err := plan.OSControlPlane(config.ReleaseNamespacedName.Name, osConfig.Image, osConfig.Version, osConfig.DrainOpts.ControlPlane)
	if err != nil {
		return nil, fmt.Errorf("generating OS control-plane plan: %w", err)
	}

	planList := []*upgradecattlev1.Plan{cpPlan}

	allNodes := &corev1.NodeList{}
	if err := r.List(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("listing cluster nodes: %w", err)
	}

	if !isControlPlaneOnlyCluster(allNodes.Items) {
		wkPlan, err := plan.OSWorker(config.ReleaseNamespacedName.Name, osConfig.Image, osConfig.Version, osConfig.DrainOpts.Worker)
		if err != nil {
			return nil, fmt.Errorf("generating OS worker plan: %w", err)
		}
		planList = append(planList, wkPlan)
	}

	return planList, nil
}
