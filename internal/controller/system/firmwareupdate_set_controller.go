// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/ironcore-dev/controller-utils/clientutils"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

const (
	FirmwareUpdateSetFinalizer = "system.metal.ironcore.dev/firmwareupdateset"
)

// FirmwareUpdateSetReconciler reconciles a FirmwareUpdateSet object
type FirmwareUpdateSetReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ResyncInterval time.Duration
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatesets/finalizers,verbs=update
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *FirmwareUpdateSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fwUpdateSet := &systemv1alpha1.FirmwareUpdateSet{}
	if err := r.Get(ctx, req.NamespacedName, fwUpdateSet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, fwUpdateSet)
}

func (r *FirmwareUpdateSetReconciler) reconcileExists(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) (ctrl.Result, error) {
	if !fwUpdateSet.DeletionTimestamp.IsZero() {
		return r.delete(ctx, fwUpdateSet)
	}
	return r.reconcile(ctx, fwUpdateSet)
}

func (r *FirmwareUpdateSetReconciler) delete(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting FirmwareUpdateSet")
	if !controllerutil.ContainsFinalizer(fwUpdateSet, FirmwareUpdateSetFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.handleIgnoreAnnotationPropagation(ctx, fwUpdateSet); err != nil {
		return ctrl.Result{}, err
	}

	fwList, err := r.getOwnedFirmwareUpdates(ctx, fwUpdateSet)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get owned FirmwareUpdates: %w", err)
	}

	status := r.getOwnedFirmwareUpdateSetStatus(fwList)
	total := int32(len(fwList.Items))
	terminalCount := status.CompletedFirmwareUpdate + status.FailedFirmwareUpdate
	oldTotal := fwUpdateSet.Status.PendingFirmwareUpdate + fwUpdateSet.Status.InProgressFirmwareUpdate +
		fwUpdateSet.Status.CompletedFirmwareUpdate + fwUpdateSet.Status.FailedFirmwareUpdate

	if total != terminalCount || oldTotal != total {
		if err = r.patchStatus(ctx, status, fwUpdateSet); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch FirmwareUpdateSet status: %w", err)
		}
		log.V(1).Info("FirmwareUpdateSet status patched", "Status", status)

		if err := r.handleRetryAnnotationPropagation(ctx, fwUpdateSet); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Waiting on the created FirmwareUpdate to reach terminal status")
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, fwUpdateSet, FirmwareUpdateSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	log.V(1).Info("Deleted FirmwareUpdateSet")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateSetReconciler) handleIgnoreAnnotationPropagation(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) error {
	log := ctrl.LoggerFrom(ctx)
	fwList, err := r.getOwnedFirmwareUpdates(ctx, fwUpdateSet)
	if err != nil {
		return err
	}

	if len(fwList.Items) == 0 {
		log.V(1).Info("No FirmwareUpdates found, skipping ignore annotation propagation")
		return nil
	}
	return utils.HandleIgnoreAnnotationPropagation(ctx, r.Client, fwUpdateSet, fwList)
}

func (r *FirmwareUpdateSetReconciler) handleRetryAnnotationPropagation(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) error {
	log := ctrl.LoggerFrom(ctx)
	fwList, err := r.getOwnedFirmwareUpdates(ctx, fwUpdateSet)
	if err != nil {
		return err
	}

	if len(fwList.Items) == 0 {
		log.V(1).Info("No FirmwareUpdate found, skipping retry annotation propagation")
		return nil
	}
	return utils.HandleRetryAnnotationPropagation(ctx, r.Client, fwUpdateSet, fwList)
}

func (r *FirmwareUpdateSetReconciler) reconcile(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling FirmwareUpdateSet")
	if err := r.handleIgnoreAnnotationPropagation(ctx, fwUpdateSet); err != nil {
		return ctrl.Result{}, err
	}

	if utils.ShouldIgnoreReconciliation(fwUpdateSet) {
		log.V(1).Info("Skipped FirmwareUpdateSet reconciliation")
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, fwUpdateSet, FirmwareUpdateSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	servers, err := r.getServersBySelector(ctx, fwUpdateSet)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get servers by selector: %w", err)
	}

	fwList, err := r.getOwnedFirmwareUpdates(ctx, fwUpdateSet)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get owned FirmwareUpdates: %w", err)
	}

	log.V(1).Info("Summary of Servers and FirmwareUpdates", "ServerCount", len(servers.Items), "FirmwareUpdateCount", len(fwList.Items))

	// Create FirmwareUpdate for servers which do not have one yet
	if err := r.ensureFirmwareUpdatesForServers(ctx, servers, fwList, fwUpdateSet); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create FirmwareUpdates: %w", err)
	}

	// Delete FirmwareUpdates which no longer have a matching server
	if err := r.deleteOrphanFirmwareUpdates(ctx, servers, fwList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete orphaned FirmwareUpdates: %w", err)
	}

	var pendingPatchingFirmwareUpdate bool
	if pendingPatchingFirmwareUpdate, err = r.patchFirmwareUpdateFromTemplate(ctx, &fwUpdateSet.Spec.FirmwareUpdateTemplate, fwList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch FirmwareUpdate spec from template: %w", err)
	}

	log.V(1).Info("Updating the status of FirmwareUpdateSet")
	status := r.getOwnedFirmwareUpdateSetStatus(fwList)
	status.MatchingServers = int32(len(servers.Items))

	if err := r.patchStatus(ctx, status, fwUpdateSet); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update FirmwareUpdateSet status: %w", err)
	}
	log.V(1).Info("Patched FirmwareUpdateSet status", "Status", status)

	if err := r.handleRetryAnnotationPropagation(ctx, fwUpdateSet); err != nil {
		return ctrl.Result{}, err
	}

	if status.MatchingServers != int32(len(fwList.Items)) || pendingPatchingFirmwareUpdate {
		log.V(1).Info("Waiting for all FirmwareUpdate to be created/Patched for the labeled Servers", "Status", status)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	log.V(1).Info("Reconciled FirmwareUpdateSet")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateSetReconciler) ensureFirmwareUpdatesForServers(ctx context.Context, servers *metalv1alpha1.ServerList, fwList *systemv1alpha1.FirmwareUpdateList, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) error {
	log := ctrl.LoggerFrom(ctx)
	withFirmwareUpdate := make(map[string]bool)
	for _, fw := range fwList.Items {
		withFirmwareUpdate[fw.Spec.ServerRef.Name] = true
	}

	maxConcurrent := fwUpdateSet.Spec.UpdateStrategy.MaxConcurrent
	inProgress := int32(0)
	if maxConcurrent > 0 {
		for _, fw := range fwList.Items {
			if fw.Status.State == systemv1alpha1.FirmwareUpdateStateInProgress {
				inProgress++
			}
		}
	}

	var errs []error
	for _, server := range servers.Items {
		if !withFirmwareUpdate[server.Name] {
			if maxConcurrent > 0 && inProgress >= maxConcurrent {
				log.V(1).Info("MaxConcurrent reached, deferring FirmwareUpdate creation", "MaxConcurrent", maxConcurrent, "InProgress", inProgress, "Server", server.Name)
				continue
			}
			// generate a deterministic k8s conform name, so that a re-reconcile on a
			// stale cache cannot create duplicate children.
			newFirmwareUpdateName := utils.VersionSetChildName(fwUpdateSet.Name, server.Name)
			newFirmwareUpdate := &systemv1alpha1.FirmwareUpdate{
				ObjectMeta: metav1.ObjectMeta{
					Name: newFirmwareUpdateName,
				}}

			opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, newFirmwareUpdate, func() error {
				newFirmwareUpdate.Spec.FirmwareUpdateTemplate = *fwUpdateSet.Spec.FirmwareUpdateTemplate.DeepCopy()
				newFirmwareUpdate.Spec.ServerRef = &corev1.LocalObjectReference{Name: server.Name}
				return controllerutil.SetControllerReference(fwUpdateSet, newFirmwareUpdate, r.Client.Scheme())
			})
			if err != nil {
				errs = append(errs, err)
			} else {
				inProgress++
				log.V(1).Info("Created FirmwareUpdate", "FirmwareUpdate", newFirmwareUpdate.Name, "Server", server.Name, "Operation", opResult)
			}
		}
	}
	return errors.Join(errs...)
}

func (r *FirmwareUpdateSetReconciler) deleteOrphanFirmwareUpdates(ctx context.Context, servers *metalv1alpha1.ServerList, fwList *systemv1alpha1.FirmwareUpdateList) error {
	log := ctrl.LoggerFrom(ctx)
	serverMap := make(map[string]bool)
	for _, server := range servers.Items {
		serverMap[server.Name] = true
	}

	var errs []error
	for _, fw := range fwList.Items {
		if !serverMap[fw.Spec.ServerRef.Name] {
			if fw.Status.State == systemv1alpha1.FirmwareUpdateStateInProgress && fw.Status.ServerMaintenanceRef != nil {
				active, err := utils.IsAnyServerMaintenanceActive(ctx, r.Client, []metalv1alpha1.ObjectReference{*fw.Status.ServerMaintenanceRef})
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to check maintenance state for FirmwareUpdate %s: %w", fw.Name, err))
					continue
				}
				if active {
					log.V(1).Info("Waiting for FirmwareUpdate maintenance to complete before deletion", "FirmwareUpdate", fw.Name, "Status", fw.Status)
					continue
				}
			}
			if err := r.Delete(ctx, &fw); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (r *FirmwareUpdateSetReconciler) patchFirmwareUpdateFromTemplate(ctx context.Context, template *systemv1alpha1.FirmwareUpdateTemplate, fwList *systemv1alpha1.FirmwareUpdateList) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if len(fwList.Items) == 0 {
		log.V(1).Info("No FirmwareUpdate found, skipping spec template update")
		return false, nil
	}

	var pendingPatchingFirmwareUpdate bool
	var errs []error
	for _, fw := range fwList.Items {
		if fw.Status.State == systemv1alpha1.FirmwareUpdateStateInProgress {
			pendingPatchingFirmwareUpdate = true
			continue
		}
		opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, &fw, func() error {
			fw.Spec.FirmwareUpdateTemplate = *template.DeepCopy()
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
		if opResult != controllerutil.OperationResultNone {
			log.V(1).Info("Patched FirmwareUpdate with updated spec", "FirmwareUpdate", fw.Name, "Operation", opResult)
		}
	}
	return pendingPatchingFirmwareUpdate, errors.Join(errs...)
}

func (r *FirmwareUpdateSetReconciler) getOwnedFirmwareUpdateSetStatus(fwList *systemv1alpha1.FirmwareUpdateList) *systemv1alpha1.FirmwareUpdateSetStatus {
	status := &systemv1alpha1.FirmwareUpdateSetStatus{}
	for _, fw := range fwList.Items {
		switch fw.Status.State {
		case systemv1alpha1.FirmwareUpdateStateCompleted:
			status.CompletedFirmwareUpdate += 1
		case systemv1alpha1.FirmwareUpdateStateFailed:
			status.FailedFirmwareUpdate += 1
		case systemv1alpha1.FirmwareUpdateStateInProgress:
			status.InProgressFirmwareUpdate += 1
		case systemv1alpha1.FirmwareUpdateStatePending, "":
			status.PendingFirmwareUpdate += 1
		}
	}
	return status
}

func (r *FirmwareUpdateSetReconciler) getOwnedFirmwareUpdates(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) (*systemv1alpha1.FirmwareUpdateList, error) {
	fwList := &systemv1alpha1.FirmwareUpdateList{}
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, fwUpdateSet, fwList); err != nil {
		return nil, err
	}
	return fwList, nil
}

func (r *FirmwareUpdateSetReconciler) getServersBySelector(ctx context.Context, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) (*metalv1alpha1.ServerList, error) {
	selector, err := metav1.LabelSelectorAsSelector(&fwUpdateSet.Spec.ServerSelector)
	if err != nil {
		return nil, err
	}
	servers := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, servers, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return servers, nil
}

func (r *FirmwareUpdateSetReconciler) patchStatus(ctx context.Context, status *systemv1alpha1.FirmwareUpdateSetStatus, fwUpdateSet *systemv1alpha1.FirmwareUpdateSet) error {
	fwUpdateSetBase := fwUpdateSet.DeepCopy()
	fwUpdateSet.Status = *status

	if err := r.Status().Patch(ctx, fwUpdateSet, client.MergeFrom(fwUpdateSetBase)); err != nil {
		return err
	}
	return nil
}

func (r *FirmwareUpdateSetReconciler) enqueueByServer(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	server := obj.(*metalv1alpha1.Server)

	setList := &systemv1alpha1.FirmwareUpdateSetList{}
	if err := r.List(ctx, setList); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateSet")
		return nil
	}
	reqs := make([]ctrl.Request, 0)
	for _, set := range setList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&set.Spec.ServerSelector)
		if err != nil {
			log.Error(err, "Failed to convert label selector")
			return nil
		}
		// If the Server label matches the selector, enqueue the request
		if selector.Matches(labels.Set(server.GetLabels())) {
			reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKey{Name: set.Name}})
		} else { // if the label has been removed
			fwList, err := r.getOwnedFirmwareUpdates(ctx, &set)
			if err != nil {
				log.Error(err, "Failed to get owned FirmwareUpdates")
				return nil
			}
			for _, fw := range fwList.Items {
				if fw.Spec.ServerRef.Name == server.Name {
					reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKey{Name: set.Name}})
				}
			}
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.FirmwareUpdateSet{}).
		Owns(&systemv1alpha1.FirmwareUpdate{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByServer),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Complete(r)
}
