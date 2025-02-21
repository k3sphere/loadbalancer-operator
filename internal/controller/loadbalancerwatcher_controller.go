package controller

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// LoadBalancerWatcherReconciler reconciles a LoadBalancerWatcher object
type LoadBalancerWatcherReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile watches for LoadBalancer Services and handles creation & deletion
func (r *LoadBalancerWatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var svc corev1.Service
	err := r.Get(ctx, req.NamespacedName, &svc)

	// 🛑 Case 1: Service was deleted
	if err != nil {
		log.Info("Service deleted, deallocating IP", "name", req.Name)
		go notifyIPDeallocation(req.Name) // Notify API asynchronously
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 🛑 Case 2: Ignore if not LoadBalancer type
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return ctrl.Result{}, nil
	}

	// 🛑 Case 3: Ignore if already has an external IP
	if len(svc.Status.LoadBalancer.Ingress) > 0 {
		return ctrl.Result{}, nil
	}

	// ✅ Case 4: Assign a new IP
	publicIP, err := getPublicIPFromAPI()
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Assigning public IP to Service", "IP", publicIP, "Service", svc.Name)

	// Patch the service with the new IP
	patch := client.MergeFrom(svc.DeepCopy())
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: publicIP}}
	if err := r.Status().Patch(ctx, &svc, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getPublicIPFromAPI calls an external API to fetch a new IP
func getPublicIPFromAPI() (string, error) {
	resp, err := http.Get("https://my-api.com/get-public-ip") // Replace with your actual API
	if err != nil {
		return "", fmt.Errorf("error calling API: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading API response: %v", err)
	}

	return string(body), nil
}

// notifyIPDeallocation calls an external API when a service is deleted
func notifyIPDeallocation(serviceName string) {
	apiURL := fmt.Sprintf("https://my-api.com/release-ip?service=%s", serviceName)
	_, err := http.Post(apiURL, "application/json", nil)
	if err != nil {
		fmt.Printf("Failed to notify IP deallocation for %s: %v\n", serviceName, err)
	} else {
		fmt.Printf("IP deallocation notified for service %s\n", serviceName)
	}
}

// SetupWithManager registers the controller with the Manager
func (r *LoadBalancerWatcherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}). // Watch Service resources
		Complete(r)
}
