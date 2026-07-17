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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

var _ = Describe("ServiceLevelObjective Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		servicelevelobjective := &srev1alpha1.ServiceLevelObjective{}

		BeforeEach(func() {
			By("creating a valid custom resource for the Kind ServiceLevelObjective")
			err := k8sClient.Get(ctx, typeNamespacedName, servicelevelobjective)
			if err != nil && errors.IsNotFound(err) {
				resource := &srev1alpha1.ServiceLevelObjective{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: srev1alpha1.ServiceLevelObjectiveSpec{
						Objective: "99.9",
						Window:    "30d",
						SLI: srev1alpha1.SLISpec{
							Type:       "availability",
							TotalQuery: `sum(rate(http_requests_total{service="test"}[{{.Window}}]))`,
							ErrorQuery: `sum(rate(http_requests_total{service="test",code=~"5.."}[{{.Window}}]))`,
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &srev1alpha1.ServiceLevelObjective{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ServiceLevelObjective")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should generate an owned PrometheusRule and set the status", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ServiceLevelObjectiveReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("checking that a PrometheusRule owned by the SLO was created")
			var pr monitoringv1.PrometheusRule
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName + "-slo-rules",
				Namespace: resourceNamespace,
			}, &pr)).To(Succeed())
			Expect(pr.OwnerReferences).To(HaveLen(1))
			Expect(pr.OwnerReferences[0].Kind).To(Equal("ServiceLevelObjective"))
			// 7 recording rules + 4 burn-rate alerts.
			Expect(pr.Spec.Groups).To(HaveLen(1))
			Expect(pr.Spec.Groups[0].Rules).To(HaveLen(11))

			By("checking that the status reports RulesGenerated=True")
			var got srev1alpha1.ServiceLevelObjective
			Expect(k8sClient.Get(ctx, typeNamespacedName, &got)).To(Succeed())
			cond := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondRulesGenerated)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})
