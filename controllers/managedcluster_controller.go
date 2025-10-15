package controllers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/red-hat-storage/odf-multicluster-orchestrator/controllers/utils"

	viewv1beta1 "github.com/stolostron/multicloud-operators-foundation/pkg/apis/view/v1beta1"
	corev1 "k8s.io/api/core/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ManagedClusterReconciler struct {
	Client           client.Client
	Logger           *slog.Logger
	testEnvFile      string
	CurrentNamespace string
}

func (r *ManagedClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Logger.Info("Setting up ManagedClusterReconciler with manager")
	managedClusterPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			obj, ok := e.ObjectNew.(*clusterv1.ManagedCluster)
			if !ok {
				return false
			}
			return utils.HasRequiredODFKey(obj)
		},
		CreateFunc: func(e event.CreateEvent) bool {
			obj, ok := e.Object.(*clusterv1.ManagedCluster)
			if !ok {
				return false
			}
			return utils.HasRequiredODFKey(obj)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.ManagedCluster{}, builder.WithPredicates(managedClusterPredicate, predicate.ResourceVersionChangedPredicate{})).
		Owns(&viewv1beta1.ManagedClusterView{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *ManagedClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := r.Logger.With("ManagedCluster", req.NamespacedName)
	logger.Info("Reconciling ManagedCluster")

	managedCluster := &clusterv1.ManagedCluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, managedCluster); err != nil {
		if client.IgnoreNotFound(err) != nil {
			logger.Error("Failed to get ManagedCluster", "error", err)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.processManagedClusterViews(ctx, managedCluster); err != nil {
		logger.Error("Failed to ensure ManagedClusterView", "error", err)
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled ManagedCluster")

	return ctrl.Result{}, nil
}

func (r *ManagedClusterReconciler) processManagedClusterViews(ctx context.Context, managedCluster *clusterv1.ManagedCluster) error {
	odfInfoConfigMapNamespacedName, err := utils.GetNamespacedNameForClusterInfo(managedCluster)
	if err != nil {
		return fmt.Errorf("error while getting NamespacedName of the ConfigMap ClusterClaim. %w", err)
	}

	odfInfoMcv := &viewv1beta1.ManagedClusterView{}
	odfInfoMcv.Name = utils.GetManagedClusterViewName(managedCluster.Name)
	odfInfoMcv.Namespace = managedCluster.Name
	operationResult, err := ctrl.CreateOrUpdate(ctx, r.Client, odfInfoMcv, func() error {
		odfInfoMcv.Spec = viewv1beta1.ViewSpec{
			Scope: viewv1beta1.ViewScope{
				Name:      odfInfoConfigMapNamespacedName.Name,
				Namespace: odfInfoConfigMapNamespacedName.Namespace,
				Resource:  "ConfigMap",
			},
		}

		utils.AddLabel(odfInfoMcv, utils.CreatedByLabelKey, utils.CreatorMulticlusterOrchestrator)
		utils.AddLabel(odfInfoMcv, utils.HubRecoveryLabel, "")

		if err := ctrl.SetControllerReference(managedCluster, odfInfoMcv, r.Client.Scheme()); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update ManagedClusterView %s. %w", client.ObjectKeyFromObject(odfInfoMcv), err)
	}
	r.Logger.Info(fmt.Sprintf("ManagedClusterView %s was %s", client.ObjectKeyFromObject(odfInfoMcv), operationResult))

	// Create ManagedClusterView for token-exchange-agent deployment running on managedclusters.
	tokenExchangeMcv := &viewv1beta1.ManagedClusterView{}
	tokenExchangeMcv.Name = utils.GetTokenExchangeManagedClusterViewName(managedCluster.Name)
	tokenExchangeMcv.Namespace = managedCluster.Name

	operationResult, err = ctrl.CreateOrUpdate(ctx, r.Client, tokenExchangeMcv, func() error {
		odfInfoMcv.Spec = viewv1beta1.ViewSpec{
			Scope: viewv1beta1.ViewScope{
				Name:      utils.TokenExchangeDeployment,
				Namespace: odfInfoConfigMapNamespacedName.Namespace,
				Resource:  "Deployment",
			},
		}

		utils.AddLabel(odfInfoMcv, utils.CreatedByLabelKey, utils.CreatorMulticlusterOrchestrator)
		utils.AddLabel(odfInfoMcv, utils.HubRecoveryLabel, "")

		if err := ctrl.SetControllerReference(managedCluster, odfInfoMcv, r.Client.Scheme()); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update ManagedClusterView %s. %w", client.ObjectKeyFromObject(tokenExchangeMcv), err)
	}
	r.Logger.Info(fmt.Sprintf("ManagedClusterView %s was %s", client.ObjectKeyFromObject(tokenExchangeMcv), operationResult))

	return nil
}
