package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Payload struct {
	Namespace      string `json:"namespace"`
	Name  string `json:"name"`
	Ports    []corev1.ServicePort `json:"ports"`
}

// LoadBalancerWatcherReconciler reconciles a LoadBalancerWatcher object
type LoadBalancerWatcherReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile watches for LoadBalancer Services and handles creation & deletion
func (r *LoadBalancerWatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// get cluster id from current node label cluster-id
	clusterID := ""
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return ctrl.Result{}, err
	}
	
	for _, node := range nodeList.Items {
		if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
			if val, ok := node.Labels["cluster-id"]; ok {
				clusterID = val
				break
			}
		}
	}
	token, err := GetServiceAccountToken()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get service account token: %v", err)
	}
	if token == "" {
		return ctrl.Result{}, fmt.Errorf("service account token is empty")
	}

	var svc corev1.Service
	err = r.Get(ctx, req.NamespacedName, &svc)

	// 🛑 Case 1: Service was deleted
	if err != nil {
		log.Info("Service deleted, deallocating IP", "name", req.Name)
		go notifyIPDeallocation(clusterID, token, req.Namespace, req.Name) // Notify API asynchronously
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 🛑 Case 2: Ignore if not LoadBalancer type
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || svc.Namespace == "kube-system" {
		return ctrl.Result{}, nil
	}

	// 🛑 Case 3: Ignore if already has an external IP
	if len(svc.Status.LoadBalancer.Ingress) > 0 {
		return ctrl.Result{}, nil
	}

	data := Payload{
		Namespace:      svc.Namespace,
		Name:    svc.Name,
		Ports:   svc.Spec.Ports,
	}
	// ✅ Case 4: Assign a new IP
	publicIP, err := getPublicIPFromAPI(clusterID, token, data)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Assigning public IP to Service", "IP", publicIP, "Service", svc.Name)

	// Patch the service with the new IP
	patch := client.MergeFrom(svc.DeepCopy())
	svc.Spec.ExternalIPs = publicIP
	//svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: publicIP}}
	if err := r.Client.Patch(ctx, &svc, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getPublicIPFromAPI calls an external API to fetch a new IP
func getPublicIPFromAPI(cluster string, token string, data Payload) ([]string, error) {
    url := fmt.Sprintf("https://k3sphere.com/api/cluster/%s/service", cluster) // Replace with your actual API


	// Marshal the data to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		fmt.Println("Error marshaling JSON:", err)
	}

	// Create a new HTTP POST request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
    if err != nil {
        return []string{}, fmt.Errorf("error creating request: %v", err)
    }

    // Set the authorization header
    req.Header.Set("Authorization", "Bearer "+token)

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return []string{}, fmt.Errorf("error calling API: %v", err)
    }
    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        return []string{}, fmt.Errorf("error reading API response: %v", err)
    }

    var responseData struct {
        IP []string `json:"ip"`
    }
    if err := json.Unmarshal(body, &responseData); err != nil {
        return []string{}, fmt.Errorf("error decoding response: %v", err)
    }

    return responseData.IP, nil
}

// notifyIPDeallocation calls an external API when a service is deleted
func notifyIPDeallocation(cluster string, token string, namespace string, serviceName string) {
    url := fmt.Sprintf("https://k3sphere.com/api/cluster/%s/service/%s:%s", cluster,namespace, serviceName) // Replace with your actual API
    req, err := http.NewRequest("DELETE", url, nil)
    if err != nil {
        fmt.Printf("error creating request: %v", err)
		return
    }

    // Set the authorization header
    req.Header.Set("Authorization", "Bearer "+token)

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
		fmt.Printf("error call api request: %v", err)
		return
    }
    defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("error response status code: %d", resp.StatusCode)
		return
	}


}

// SetupWithManager registers the controller with the Manager
func (r *LoadBalancerWatcherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}). // Watch Service resources
		Complete(r)
}


// GetServiceAccountToken reads the token from the mounted service account secret
func GetServiceAccountToken() (string, error) {
	const tokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	
	token, err := ioutil.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to read service account token: %w", err)
	}
	return string(token), nil
}