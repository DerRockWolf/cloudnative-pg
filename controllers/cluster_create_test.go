/*
Copyright The CloudNativePG Contributors

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

package controllers

import (
	"context"

	v1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/testing"
	k8client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/internal/configuration"
	schemeBuilder "github.com/cloudnative-pg/cloudnative-pg/internal/scheme"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/specs"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cluster_create unit tests", func() {
	It("should make sure that reconcilePostgresSecrets works correctly", func() {
		ctx := context.Background()
		namespace := newFakeNamespace()
		cluster := newFakeCNPGCluster(namespace)
		pooler := newFakePooler(cluster)
		poolerSecretName := pooler.Name
		cluster.Status.PoolerIntegrations = &apiv1.PoolerIntegrations{
			PgBouncerIntegration: apiv1.PgBouncerIntegrationStatus{
				Secrets: []string{poolerSecretName},
			},
		}

		By("creating prerequisites", func() {
			generateFakeCASecretWithDefaultClient(cluster.GetClientCASecretName(), namespace, "testdomain.com")
		})

		By("executing reconcilePostgresSecrets", func() {
			err := clusterReconciler.reconcilePostgresSecrets(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		By("making sure that the superUser secret have been created", func() {
			superUser := corev1.Secret{}
			err := k8sClient.Get(
				ctx,
				types.NamespacedName{Name: cluster.GetSuperuserSecretName(), Namespace: namespace},
				&superUser,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		By("making sure that the appUserSecret have been created", func() {
			appUser := corev1.Secret{}
			err := k8sClient.Get(
				ctx,
				types.NamespacedName{Name: cluster.GetApplicationSecretName(), Namespace: namespace},
				&appUser,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		By("making sure that the pooler secrets have been created", func() {
			poolerSecret := corev1.Secret{}
			err := k8sClient.Get(
				ctx,
				types.NamespacedName{Name: poolerSecretName, Namespace: namespace},
				&poolerSecret,
			)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	It("should make sure that reconcilePostgresServices works correctly", func() {
		ctx := context.Background()
		namespace := newFakeNamespace()
		cluster := newFakeCNPGCluster(namespace)

		By("executing reconcilePostgresServices", func() {
			err := clusterReconciler.reconcilePostgresServices(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		By("making sure that the services have been created", func() {
			expectResourceExistsWithDefaultClient(cluster.GetServiceReadOnlyName(), namespace, &corev1.Service{})
			expectResourceExistsWithDefaultClient(cluster.GetServiceReadWriteName(), namespace, &corev1.Service{})
			expectResourceExistsWithDefaultClient(cluster.GetServiceReadName(), namespace, &corev1.Service{})
		})
	})

	It("should make sure that reconcilePostgresServices works correctly if create any service is enabled", func() {
		ctx := context.Background()
		namespace := newFakeNamespace()
		cluster := newFakeCNPGCluster(namespace)
		configuration.Current.CreateAnyService = true

		By("executing reconcilePostgresServices", func() {
			err := clusterReconciler.reconcilePostgresServices(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		By("making sure that the services have been created", func() {
			expectResourceExistsWithDefaultClient(cluster.GetServiceAnyName(), namespace, &corev1.Service{})
			expectResourceExistsWithDefaultClient(cluster.GetServiceReadOnlyName(), namespace, &corev1.Service{})
			expectResourceExistsWithDefaultClient(cluster.GetServiceReadWriteName(), namespace, &corev1.Service{})
			expectResourceExistsWithDefaultClient(cluster.GetServiceReadName(), namespace, &corev1.Service{})
		})
	})

	It("should make sure that reconcilePostgresServices can update the selectors on existing services", func() {
		ctx := context.Background()
		namespace := newFakeNamespace()
		cluster := newFakeCNPGCluster(namespace)
		configuration.Current.CreateAnyService = true

		createOutdatedService := func(svc *corev1.Service) {
			cluster.SetInheritedDataAndOwnership(&svc.ObjectMeta)
			svc.Spec.Selector = map[string]string{
				"outdated": "selector",
			}
			err := clusterReconciler.Client.Create(ctx, svc)
			Expect(err).ToNot(HaveOccurred())
		}

		checkService := func(before *corev1.Service, expectedLabels map[string]string) {
			var afterChangesService corev1.Service
			err := clusterReconciler.Client.Get(ctx, types.NamespacedName{
				Name:      before.Name,
				Namespace: before.Namespace,
			}, &afterChangesService)
			Expect(err).ToNot(HaveOccurred())

			Expect(afterChangesService.Spec.Selector).ToNot(Equal(before.Spec.Selector))
			Expect(afterChangesService.Spec.Selector).To(Equal(expectedLabels))
			Expect(afterChangesService.Labels).To(Equal(before.Labels))
			Expect(afterChangesService.Annotations).To(Equal(before.Annotations))
		}

		var readOnlyService, readWriteService, readService, anyService *corev1.Service
		By("creating the resources with outdated selectors", func() {
			By("creating any service", func() {
				svc := specs.CreateClusterAnyService(*cluster)
				createOutdatedService(svc)
				anyService = svc.DeepCopy()
			})

			By("creating read service", func() {
				svc := specs.CreateClusterReadService(*cluster)
				createOutdatedService(svc)
				readService = svc.DeepCopy()
			})

			By("creating read-write service", func() {
				svc := specs.CreateClusterReadWriteService(*cluster)
				createOutdatedService(svc)
				readWriteService = svc.DeepCopy()
			})
			By("creating read only service", func() {
				svc := specs.CreateClusterReadOnlyService(*cluster)
				createOutdatedService(svc)
				readOnlyService = svc.DeepCopy()
			})
		})

		By("executing reconcilePostgresServices", func() {
			err := clusterReconciler.reconcilePostgresServices(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		By("checking any service", func() {
			checkService(anyService, map[string]string{
				"cnpg.io/podRole": "instance",
				"cnpg.io/cluster": cluster.Name,
			})
		})

		By("checking read-write service", func() {
			checkService(readWriteService, map[string]string{
				"cnpg.io/cluster": cluster.Name,
				"role":            "primary",
			})
		})

		By("checking read service", func() {
			checkService(readService, map[string]string{
				"cnpg.io/cluster": cluster.Name,
				"cnpg.io/podRole": "instance",
			})
		})

		By("checking read only service", func() {
			checkService(readOnlyService, map[string]string{
				"cnpg.io/cluster": cluster.Name,
				"role":            "replica",
			})
		})
	})

	It("should make sure that createOrPatchServiceAccount works correctly", func() {
		ctx := context.Background()
		namespace := newFakeNamespace()
		cluster := newFakeCNPGCluster(namespace)

		By("executing createOrPatchServiceAccount (create)", func() {
			err := clusterReconciler.createOrPatchServiceAccount(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		sa := &corev1.ServiceAccount{}

		By("making sure that the serviceaccount has been created", func() {
			expectResourceExistsWithDefaultClient(cluster.Name, namespace, sa)
		})

		By("adding an annotation, a label and an image pull secret to the service account", func() {
			sa.Annotations["test"] = "annotation"
			sa.Labels["test"] = "label"
			sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{
				Name: "sa-pullsecret",
			})
			err := k8sClient.Update(context.Background(), sa)
			Expect(err).ToNot(HaveOccurred())
		})

		By("executing createOrPatchServiceAccount (no-patch)", func() {
			err := clusterReconciler.createOrPatchServiceAccount(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		By("making sure that the serviceaccount is untouched because there is no change in the cluster", func() {
			updatedSa := &corev1.ServiceAccount{}
			expectResourceExistsWithDefaultClient(cluster.Name, namespace, updatedSa)
			Expect(updatedSa).To(BeEquivalentTo(sa))
		})

		By("adding an image pull secret to the cluster to trigger a service account update", func() {
			cluster.Spec.ImagePullSecrets = append(cluster.Spec.ImagePullSecrets, apiv1.LocalObjectReference{
				Name: "cluster-pullsecret",
			})
			err := k8sClient.Update(context.Background(), cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		By("executing createOrPatchServiceAccount (patch)", func() {
			By("setting owner reference to nil", func() {
				sa.ObjectMeta.OwnerReferences = nil
				err := k8sClient.Update(context.Background(), sa)
				Expect(err).ToNot(HaveOccurred())
			})

			By("running patch", func() {
				err := clusterReconciler.createOrPatchServiceAccount(ctx, cluster)
				Expect(err).ToNot(HaveOccurred())
			})

			By("making sure that the serviceaccount is patched correctly", func() {
				updatedSA := &corev1.ServiceAccount{}
				expectResourceExistsWithDefaultClient(cluster.Name, namespace, updatedSA)
				Expect(updatedSA.Annotations["test"]).To(BeEquivalentTo("annotation"))
				Expect(updatedSA.Labels["test"]).To(BeEquivalentTo("label"))
				Expect(updatedSA.ImagePullSecrets).To(ContainElements(corev1.LocalObjectReference{
					Name: "cluster-pullsecret",
				}))
				Expect(updatedSA.ImagePullSecrets).To(ContainElements(corev1.LocalObjectReference{
					Name: "sa-pullsecret",
				}))
				Expect(updatedSA.OwnerReferences).To(BeNil())
			})
		})
	})

	It("should make sure that reconcilePodDisruptionBudget works correctly", func() {
		ctx := context.Background()
		namespace := newFakeNamespace()
		cluster := newFakeCNPGCluster(namespace)
		pdbReplicaName := specs.BuildReplicasPodDisruptionBudget(cluster).Name
		pdbPrimaryName := specs.BuildPrimaryPodDisruptionBudget(cluster).Name
		reconcilePDB := func() {
			err := clusterReconciler.reconcilePodDisruptionBudget(ctx, cluster)
			Expect(err).ToNot(HaveOccurred())
		}

		By("creating the primary and replica PDB in a standard case scenario", func() {
			reconcilePDB()
		})

		By("making sure PDB exists", func() {
			expectResourceExistsWithDefaultClient(
				pdbPrimaryName,
				namespace,
				&policyv1.PodDisruptionBudget{},
			)
			expectResourceExistsWithDefaultClient(
				pdbReplicaName,
				namespace,
				&policyv1.PodDisruptionBudget{},
			)
		})

		By("enabling the cluster maintenance mode", func() {
			reusePVC := true
			cluster.Spec.NodeMaintenanceWindow = &apiv1.NodeMaintenanceWindow{
				InProgress: true,
				ReusePVC:   &reusePVC,
			}
		})

		By("reconciling pdb during the maintenance mode", func() {
			reconcilePDB()
		})

		By("making sure that the replicas PDB are deleted", func() {
			expectResourceDoesntExistWithDefaultClient(
				pdbReplicaName,
				namespace,
				&policyv1.PodDisruptionBudget{},
			)
		})

		By("scaling the instances to 1 during maintenance mode", func() {
			cluster.Spec.Instances = 1
			cluster.Status.Instances = 1
		})

		By("reconciling pdb during the maintenance mode with a single node", func() {
			reconcilePDB()
		})

		By("making sure that both the replicas and main PDB are deleted", func() {
			expectResourceDoesntExistWithDefaultClient(
				pdbPrimaryName,
				namespace,
				&policyv1.PodDisruptionBudget{},
			)
			expectResourceDoesntExistWithDefaultClient(
				pdbReplicaName,
				namespace,
				&policyv1.PodDisruptionBudget{},
			)
		})
	})
})

var _ = Describe("Set cluster metadata of service account", func() {
	It("must be idempotent, if metadata are not defined", func() {
		sa := &corev1.ServiceAccount{}

		cluster := &apiv1.Cluster{}

		cluster.Spec.ServiceAccountTemplate.MergeMetadata(sa)
		Expect(sa.Annotations).To(BeEmpty())
		Expect(sa.Labels).To(BeEmpty())
	})

	It("must set metadata, if they are defined", func() {
		sa := &corev1.ServiceAccount{}

		annotations := map[string]string{
			"testProvider": "testAnnotation",
		}
		labels := map[string]string{
			"testProvider": "testLabel",
		}
		cluster := &apiv1.Cluster{
			Spec: apiv1.ClusterSpec{
				ServiceAccountTemplate: &apiv1.ServiceAccountTemplate{
					Metadata: apiv1.Metadata{
						Labels:      labels,
						Annotations: annotations,
					},
				},
			},
		}

		cluster.Spec.ServiceAccountTemplate.MergeMetadata(sa)
		Expect(sa.Annotations).To(BeEquivalentTo(cluster.Spec.ServiceAccountTemplate.Metadata.Annotations))
		Expect(sa.Labels).To(BeEquivalentTo(cluster.Spec.ServiceAccountTemplate.Metadata.Labels))
	})
})

type mockPodMonitorManager struct {
	isEnabled  bool
	podMonitor *v1.PodMonitor
}

func (m *mockPodMonitorManager) IsPodMonitorEnabled() bool {
	return m.isEnabled
}

func (m *mockPodMonitorManager) BuildPodMonitor() *v1.PodMonitor {
	return m.podMonitor
}

var _ = Describe("CreateOrPatchPodMonitor", func() {
	var (
		ctx                 context.Context
		fakeCli             k8client.Client
		fakeDiscoveryClient discovery.DiscoveryInterface
		manager             *mockPodMonitorManager
	)

	BeforeEach(func() {
		ctx = context.Background()
		manager = &mockPodMonitorManager{}
		manager.isEnabled = true
		manager.podMonitor = &v1.PodMonitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "default",
			},
		}

		fakeCli = fake.NewClientBuilder().WithScheme(schemeBuilder.BuildWithAllKnownScheme()).Build()

		fakeDiscoveryClient = &fakediscovery.FakeDiscovery{
			Fake: &testing.Fake{
				Resources: []*metav1.APIResourceList{
					{
						GroupVersion: "monitoring.coreos.com/v1",
						APIResources: []metav1.APIResource{
							{
								Name:       "podmonitors",
								Kind:       "PodMonitor",
								Namespaced: true,
							},
						},
					},
				},
			},
		}
	})

	It("should create the PodMonitor  when it is enabled and doesn't already exists", func() {
		err := createOrPatchPodMonitor(ctx, fakeCli, fakeDiscoveryClient, manager)
		Expect(err).ToNot(HaveOccurred())

		podMonitor := &v1.PodMonitor{}
		err = fakeCli.Get(
			ctx,
			types.NamespacedName{
				Name:      manager.podMonitor.Name,
				Namespace: manager.podMonitor.Namespace,
			},
			podMonitor,
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(podMonitor.Name).To(Equal(manager.podMonitor.Name))
		Expect(podMonitor.Namespace).To(Equal(manager.podMonitor.Namespace))
	})

	It("should not return an error when PodMonitor is disabled", func() {
		manager.isEnabled = false
		err := createOrPatchPodMonitor(ctx, fakeCli, fakeDiscoveryClient, manager)
		Expect(err).ToNot(HaveOccurred())
	})

	It("should remove the PodMonitor if it is disabled when the PodMonitor exists", func() {
		// Create the PodMonitor with the fake client
		err := fakeCli.Create(ctx, manager.podMonitor)
		Expect(err).ToNot(HaveOccurred())

		manager.isEnabled = false
		err = createOrPatchPodMonitor(ctx, fakeCli, fakeDiscoveryClient, manager)
		Expect(err).ToNot(HaveOccurred())

		// Ensure the PodMonitor doesn't exist anymore
		podMonitor := &v1.PodMonitor{}
		err = fakeCli.Get(
			ctx,
			types.NamespacedName{
				Name:      manager.podMonitor.Name,
				Namespace: manager.podMonitor.Namespace,
			},
			podMonitor,
		)
		Expect(err).To(HaveOccurred())
		Expect(apierrs.IsNotFound(err)).To(BeTrue())
	})

	It("should patch the PodMonitor with updated labels and annotations", func() {
		initialLabels := map[string]string{"label1": "value1"}
		initialAnnotations := map[string]string{"annotation1": "value1"}

		manager.podMonitor.Labels = initialLabels
		manager.podMonitor.Annotations = initialAnnotations
		err := fakeCli.Create(ctx, manager.podMonitor)
		Expect(err).ToNot(HaveOccurred())

		updatedLabels := map[string]string{"label1": "changedValue1", "label2": "value2"}
		updatedAnnotations := map[string]string{"annotation1": "changedValue1", "annotation2": "value2"}

		manager.podMonitor.Labels = updatedLabels
		manager.podMonitor.Annotations = updatedAnnotations

		err = createOrPatchPodMonitor(ctx, fakeCli, fakeDiscoveryClient, manager)
		Expect(err).ToNot(HaveOccurred())

		podMonitor := &v1.PodMonitor{}
		err = fakeCli.Get(
			ctx,
			types.NamespacedName{
				Name:      manager.podMonitor.Name,
				Namespace: manager.podMonitor.Namespace,
			},
			podMonitor,
		)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMonitor.Labels).To(Equal(updatedLabels))
		Expect(podMonitor.Annotations).To(Equal(updatedAnnotations))
	})
})
