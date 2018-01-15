package main

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	v1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	v1types "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

/////////////////////////////
// TODO(jchaloup):
// - make a queue of unhealthy nodes to be processed (one node at a time)
// - define configurable actions to be performed for unehalthy nodes
// - ...

type Controller struct {
	nodeInformer v1informers.NodeInformer
}

func NewController(kubeInformerFactory kubeinformers.SharedInformerFactory) *Controller {
	c := &Controller{
		nodeInformer: kubeInformerFactory.Core().V1().Nodes(),
	}

	c.nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.processNodes()
		},
		UpdateFunc: func(old, new interface{}) {
			c.processNodes()
		},
		DeleteFunc: func(obj interface{}) {
			c.processNodes()
		},
	})

	return c
}

func (c *Controller) processNodes() error {
	nodes, err := c.nodeInformer.Lister().List(labels.Everything())
	if err != nil {
		return err
	}
	for _, node := range nodes {
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
	return nil
}

func main() {

	config, err := clientcmd.BuildConfigFromFlags("", "/var/run/kubernetes/admin.kubeconfig")
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(clientset, time.Second*30)

	NewController(kubeInformerFactory)

	stopCh := make(chan struct{})
	go kubeInformerFactory.Start(stopCh)
	time.Sleep(2 * time.Minute)
	close(stopCh)
}
