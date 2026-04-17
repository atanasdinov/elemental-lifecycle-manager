/*
Copyright 2025-2026.

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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Release Webhook", func() {
	Context("When creating Release under Validating Webhook", Ordered, func() {
		It("Should be denied if registry is not specified", func() {
			release := &Release{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "release",
					Namespace: "default",
				},
			}

			err := k8sClient.Create(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("release registry is required")))
		})

		It("Should be denied if release version is not specified", func() {
			release := &Release{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "release",
					Namespace: "default",
				},
				Spec: ReleaseSpec{
					Registry: "registry.example.com",
				},
			}

			err := k8sClient.Create(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("release version is required")))
		})

		It("Should be denied if release version is not in semantic format", func() {
			release := &Release{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "release",
					Namespace: "default",
				},
				Spec: ReleaseSpec{
					Registry: "registry.example.com",
					Version:  "v1",
				},
			}

			err := k8sClient.Create(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("'v1' is not a semantic version")))
		})

		It("Should admit if all required fields are provided", func() {
			release := &Release{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "release",
					Namespace: "default",
				},
				Spec: ReleaseSpec{
					Registry: "registry.example.com",
					Version:  "0.5.0",
				},
			}

			Expect(k8sClient.Create(ctx, release)).To(Succeed())
		})

		It("Should be denied if release already exists", func() {
			release := &Release{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "release2",
					Namespace: "default",
				},
				Spec: ReleaseSpec{
					Registry: "registry.example.com",
					Version:  "0.5.0",
				},
			}

			err := k8sClient.Create(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("cannot create release release2. The cluster has an already created Release object: default/release")))
		})
	})

	Context("When updating Release under Validating Webhook", Ordered, func() {
		var release *Release

		BeforeEach(func() {
			release = &Release{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "release", Namespace: "default"}, release)).To(Succeed())
		})

		It("Should be denied if release registry is not specified", func() {
			release.Spec.Registry = ""

			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("release registry is required")))
		})

		It("Should be denied if release version is not specified", func() {
			release.Spec.Version = ""

			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("release version is required")))
		})

		It("Should be denied when an upgrade is pending", func() {
			condition := metav1.Condition{Type: ConditionApplied, Status: metav1.ConditionFalse, Reason: UpgradePending}

			meta.SetStatusCondition(&release.Status.Conditions, condition)
			Expect(k8sClient.Status().Update(ctx, release)).To(Succeed())

			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("cannot edit while upgrade is in 'Pending' state")))
		})

		It("Should be denied when an upgrade is in progress", func() {
			condition := metav1.Condition{Type: ConditionApplied, Status: metav1.ConditionFalse, Reason: UpgradeInProgress}

			meta.SetStatusCondition(&release.Status.Conditions, condition)
			Expect(k8sClient.Status().Update(ctx, release)).To(Succeed())

			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("cannot edit while upgrade is in 'InProgress' state")))
		})

		It("Should pass if the last update has failed, but finished", func() {
			condition := metav1.Condition{Type: ConditionApplied, Status: metav1.ConditionFalse, Reason: UpgradeFailed}

			meta.SetStatusCondition(&release.Status.Conditions, condition)
			Expect(k8sClient.Status().Update(ctx, release)).To(Succeed())

			Expect(k8sClient.Update(ctx, release)).To(Succeed())
		})

		It("Should be denied if release version is not in semantic format", func() {
			release.Spec.Version = "v1"

			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("'v1' is not a semantic version")))
		})

		It("Should be denied if the new release version is the same as the last applied one", func() {
			release.Status.Version = "1.0.0"
			Expect(k8sClient.Status().Update(ctx, release)).To(Succeed())

			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("new version must be greater than the currently applied one ('1.0.0')")))
		})

		It("Should be denied if the new release version is lower than the last applied one", func() {
			release.Spec.Version = "0.6.0"
			err := k8sClient.Update(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("new version must be greater than the currently applied one ('1.0.0')")))
		})

		It("Should pass if the new release version is higher than the last applied one", func() {
			release.Spec.Version = "1.0.1"
			Expect(k8sClient.Update(ctx, release)).To(Succeed())
		})
	})

	Context("When deleting Release under Validating Webhook", Ordered, func() {
		release := &Release{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: ReleaseSpec{
				Registry: "registry.example.com",
				Version:  "1.0.0",
			},
		}

		It("Should be denied", func() {
			err := k8sClient.Delete(ctx, release)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("deleting release objects is not allowed")))
		})
	})

})
