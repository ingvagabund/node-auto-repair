package main

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1types "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	config, err := clientcmd.BuildConfigFromFlags("", "/var/run/kubernetes/admin.kubeconfig")
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	nodes, err := clientset.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, node := range nodes.Items {
		fmt.Printf("Checking %q node...\n", node.ObjectMeta.Name)

		// Check if the node is UnReady
		var NodeReadyCondition v1types.NodeCondition
		for _, c := range node.Status.Conditions {
			if c.Type == v1types.NodeReady {
				NodeReadyCondition = c
				break
			}
		}
		if NodeReadyCondition.Status == v1types.ConditionFalse {
			fmt.Printf("LastTransitionTime: %v\n", NodeReadyCondition.LastTransitionTime.Time)
			if NodeReadyCondition.LastTransitionTime.Time.Add(10 * time.Minute).Before(time.Now()) {
				fmt.Printf("Node %q UnReady for at least 10 minutes\n", node.ObjectMeta.Name)
			} else {
				fmt.Printf("Node %q UnReady for less than 10 minutes\n", node.ObjectMeta.Name)
			}
		} else {
			fmt.Printf("Node %q Ready\n", node.ObjectMeta.Name)
		}
	}

}
