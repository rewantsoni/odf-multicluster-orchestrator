package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/red-hat-storage/odf-multicluster-orchestrator/controllers/utils"

	ocsv1alpha1 "github.com/red-hat-storage/ocs-operator/api/v4/v1alpha1"
	viewv1beta1 "github.com/stolostron/multicloud-operators-foundation/pkg/apis/view/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"
)

type ManagedClusterViewReconciler struct {
	Client           client.Client
	Logger           *slog.Logger
	testEnvFile      string
	CurrentNamespace string
}

func (r *ManagedClusterViewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Logger.Info("Setting up ManagedClusterViewReconciler with manager")
	managedClusterViewPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			obj, ok := e.ObjectNew.(*viewv1beta1.ManagedClusterView)
			if !ok {
				return false
			}
			return hasODFInfoInScope(obj)
		},
		CreateFunc: func(e event.CreateEvent) bool {
			obj, ok := e.Object.(*viewv1beta1.ManagedClusterView)
			if !ok {
				return false
			}
			return hasODFInfoInScope(obj)
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(_ event.TypedGenericEvent[client.Object]) bool {
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&viewv1beta1.ManagedClusterView{}, builder.WithPredicates(managedClusterViewPredicate, predicate.ResourceVersionChangedPredicate{})).
		Complete(r)
}

func hasODFInfoInScope(mc *viewv1beta1.ManagedClusterView) bool {
	if mc.Spec.Scope.Name == utils.ODFInfoConfigMapName && mc.Spec.Scope.Resource == "ConfigMap" {
		return true
	}
	return false
}

func (r *ManagedClusterViewReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := r.Logger.With("ManagedClusterView", req.NamespacedName)
	logger.Info("Reconciling ManagedClusterView")

	managedClusterView := &viewv1beta1.ManagedClusterView{}
	if err := r.Client.Get(ctx, req.NamespacedName, managedClusterView); err != nil {
		if client.IgnoreNotFound(err) != nil {
			logger.Error("Failed to get ManagedClusterView", "error", err)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err := r.reconcileODFClientInfoCM(ctx, managedClusterView); err != nil {
		logger.Error("Failed to create or update ConfigMap for ManagedClusterView", "error", err)
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled ManagedClusterView")

	return ctrl.Result{}, nil
}

func (r *ManagedClusterViewReconciler) reconcileODFClientInfoCM(ctx context.Context, managedClusterView *viewv1beta1.ManagedClusterView) error {
	// Initialize an empty map to hold the result data.
	var resultData map[string]interface{}
	if err := json.Unmarshal(managedClusterView.Status.Result.Raw, &resultData); err != nil {
		return fmt.Errorf("failed to unmarshal result data. %w", err)
	}

	data, ok := resultData["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("unexpected data format in result: %v", resultData["data"])
	}

	clientInfoMap := make(map[string]string)

	for key, value := range data {
		if !strings.Contains(key, ".yaml") {
			continue
		}

		yamlContent, ok := value.(string)
		if !ok {
			return fmt.Errorf("unexpected value format in data for key %s: expected string, got %T", key, value)
		}
		odfInfo := &ocsv1alpha1.OdfInfoData{}
		if err := yaml.Unmarshal([]byte(yamlContent), odfInfo); err != nil {
			return fmt.Errorf("failed to unmarshal ODF info data for key %s: %w", key, err)
		}

		providerPublicEndpoint := odfInfo.StorageCluster.Annotations[ocsv1alpha1.ApiServerExportedAddressAnnotationName]
		if providerPublicEndpoint == "" {
			r.Logger.Info("StorageProviderPublicEndpoint is not available.")
		}

		cephblockPoolsInfo := []utils.InfoCephBlockPool{}
		for _, cephblockpool := range odfInfo.StorageCluster.InfoCephBlockPools {
			cephblockPoolsInfo = append(cephblockPoolsInfo, utils.InfoCephBlockPool{
				Name:          cephblockpool.Name,
				MirrorEnabled: cephblockpool.MirrorEnabled,
			})
		}

		providerInfo := utils.ProviderInfo{
			Version:                       odfInfo.Version,
			DeploymentType:                odfInfo.DeploymentType,
			CephClusterFSID:               odfInfo.StorageCluster.CephClusterFSID,
			StorageProviderEndpoint:       odfInfo.StorageCluster.StorageProviderEndpoint,
			NamespacedName:                odfInfo.StorageCluster.NamespacedName,
			ProviderManagedClusterName:    managedClusterView.Namespace,
			StorageProviderPublicEndpoint: providerPublicEndpoint,
		}

		if len(cephblockPoolsInfo) > 0 {
			providerInfo.InfoCephBlockPools = cephblockPoolsInfo
		}

		if len(odfInfo.Clients) == 0 {
			clientInfo := utils.ClientInfo{
				ClusterID:                "",
				Name:                     "",
				ProviderInfo:             providerInfo,
				ClientManagedClusterName: "",
				ClientID:                 "",
			}
			clientInfoJSON, err := json.Marshal(clientInfo)
			if err != nil {
				return fmt.Errorf("failed to marshal client info for key %s: %w", key, err)
			}

			clientInfoMap[utils.GetKey(managedClusterView.Namespace, odfInfo.StorageCluster.NamespacedName.Name)] = string(clientInfoJSON)
		} else {
			for _, client := range odfInfo.Clients {
				managedCluster, err := utils.GetManagedClusterById(ctx, r.Client, client.ClusterID)
				if err != nil {
					if errors.IsNotFound(err) {
						r.Logger.Info(fmt.Sprintf("Managed Cluster with id %s not found", client.ClusterID), err.Error())
						continue
					}
					return err
				}
				clientInfo := utils.ClientInfo{
					ClusterID:                client.ClusterID,
					Name:                     client.Name,
					ProviderInfo:             providerInfo,
					ClientManagedClusterName: managedCluster.Name,
					ClientID:                 client.ClientID,
				}
				clientInfoJSON, err := json.Marshal(clientInfo)
				if err != nil {
					return fmt.Errorf("failed to marshal client info for key %s: %w", key, err)
				}

				clientInfoMap[utils.GetKey(managedCluster.Name, client.Name)] = string(clientInfoJSON)
			}
		}
	}

	configMap := &corev1.ConfigMap{}
	configMap.Name = utils.ClientInfoConfigMapName
	configMap.Namespace = r.CurrentNamespace

	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(configMap), configMap); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get ConfigMap. %w", err)
	}

	if configMap.Data == nil {
		configMap.Data = make(map[string]string)
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		utils.AddLabel(configMap, utils.HubRecoveryLabel, "")
		if err := controllerutil.SetOwnerReference(managedClusterView, configMap, r.Client.Scheme()); err != nil {
			return err
		}

		if configMap.Data == nil {
			configMap.Data = make(map[string]string)
		}

		for clientKey, clientInfo := range clientInfoMap {
			configMap.Data[clientKey] = clientInfo
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update ConfigMap. %w", err)
	}

	r.Logger.Info(fmt.Sprintf("ConfigMap %s in namespace %s has been %s", utils.ClientInfoConfigMapName, r.CurrentNamespace, op))
	return nil
}
