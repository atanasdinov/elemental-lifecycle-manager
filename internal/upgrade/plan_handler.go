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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	upgradecattlev1 "github.com/rancher/system-upgrade-controller/pkg/apis/upgrade.cattle.io/v1"
	lifecyclev1alpha1 "github.com/suse/elemental-lifecycle-manager/api/v1alpha1"
)

type planHandler struct {
	client.Client
}

func (r *planHandler) getOrCreatePlan(ctx context.Context, desired *upgradecattlev1.Plan) (*upgradecattlev1.Plan, error) {
	logger := log.FromContext(ctx)

	existing := &upgradecattlev1.Plan{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      desired.Name,
		Namespace: desired.Namespace,
	}, existing)

	if apierrors.IsNotFound(err) {
		logger.Info("Creating SUC Plan", "name", desired.Name)
		if err = r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return desired, nil
	}

	if err != nil {
		return nil, err
	}

	return existing, nil
}

func (r *planHandler) listNodesForPlan(ctx context.Context, p *upgradecattlev1.Plan) (nodes *corev1.NodeList, err error) {
	selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing node selector: %w", err)
	}

	nodes = &corev1.NodeList{}
	if err := r.List(ctx, nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("listing nodes with selector %s: %w", selector, err)
	}

	return nodes, nil
}

func (r *planHandler) reconcilePlan(ctx context.Context, planTemplate *upgradecattlev1.Plan) (status *PhaseStatus, err error) {
	activePlan, err := r.getOrCreatePlan(ctx, planTemplate)
	if err != nil {
		return nil, fmt.Errorf("attempting to retrieve or create %s: %w", planTemplate.Name, err)
	}

	return parsePhaseStatusFromPlan(activePlan), nil
}

func parsePhaseStatusFromPlan(p *upgradecattlev1.Plan) *PhaseStatus {
	if len(p.Status.Applying) > 0 {
		return &PhaseStatus{
			State:   lifecyclev1alpha1.UpgradeInProgress,
			Message: fmt.Sprintf("Plan '%s' is currently applying on: %s", p.Name, p.Status.Applying),
		}
	}

	for _, cond := range p.Status.Conditions {
		if cond.Type == string(upgradecattlev1.PlanComplete) {
			if cond.Status == corev1.ConditionTrue {
				return &PhaseStatus{
					State:   lifecyclev1alpha1.PlanComplete,
					Message: fmt.Sprintf("Plan '%s' execution completed successfully", p.Name),
				}
			}
			if cond.Status == corev1.ConditionFalse && cond.Reason != "" {
				return &PhaseStatus{
					State:   lifecyclev1alpha1.UpgradeFailed,
					Message: fmt.Sprintf("Plan '%s' failed: %s", p.Name, cond.Message),
				}
			}
		}
	}

	return &PhaseStatus{
		State:   lifecyclev1alpha1.UpgradeInProgress,
		Message: fmt.Sprintf("Plan '%s' execution in progress", p.Name),
	}
}
