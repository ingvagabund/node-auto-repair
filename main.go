package main

import (
	"fmt"
	"time"

	"github.com/golang/glog"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/util/workqueue"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	v1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/pkg/api"
	_ "k8s.io/client-go/pkg/api/install"
	"k8s.io/client-go/pkg/api/v1"
	v1types "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

/////////////////////////////
// TODO(jchaloup):
// - make a queue of unhealthy nodes to be processed (one node at a time)
// - define configurable actions to be performed for unehalthy nodes
// - ...

const MIN_UNREADY_DELAY = 10 * time.Minute

type Controller struct {
	kubeclientset      kubernetes.Interface
	nodeInformerLister v1listers.NodeLister
	nodeInformerSynced cache.InformerSynced
	workqueue          workqueue.DelayingInterface
}

func NewController(kubeclientset kubernetes.Interface, kubeInformerFactory kubeinformers.SharedInformerFactory) *Controller {
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()

	c := &Controller{
		kubeclientset:      kubeclientset,
		nodeInformerLister: nodeInformer.Lister(),
		nodeInformerSynced: nodeInformer.Informer().HasSynced,
		workqueue:          workqueue.NewDelayingQueue(),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// If a node is added, it is most likely in Ready state and any change
			// gets propagated through update event, so we can ignore this
			fmt.Printf("\n\nAdding node: %#v\n", obj)
			c.handleObject(obj)
		},
		UpdateFunc: func(old, new interface{}) {
			fmt.Printf("\n\nUpdating node: %#v\n", new)
			c.handleObject(new)
		},
		DeleteFunc: func(obj interface{}) {
			// If a node is added, it is most likely in Ready state and any change
			// gets propagated through update event, so we can ignore this
			fmt.Printf("\n\nDeleting node: %#v\n", obj)
			c.handleObject(obj)
		},
	})

	return c
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s': %s", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		glog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (c *Controller) syncHandler(key string) error {
	// Based on an action table either restart or remove a node
	fmt.Printf("Processing %v\n", key)

	node, err := c.nodeInformerLister.Get(key)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("foo '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	fmt.Printf("Node %v, unschedulable: %v\n", key, node.Spec.Unschedulable)

	for _, condition := range node.Status.Conditions {
		if condition.Type == v1types.NodeReady {
			if condition.Status == v1types.ConditionTrue {
				// Node is in Ready state again => ignore it
				return nil
			}
		}
	}

	// Make a copy of a node so the cache is not invalidated
	copyObj, err := api.Scheme.DeepCopy(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Unable to duplicate a node %q: %v", node.ObjectMeta.Name, err))
		return nil
	}
	nodeCopy := copyObj.(*v1.Node)

	// 1. mark a node unschedulable
	nodeCopy.Spec.Unschedulable = true

	// oldData, err := json.Marshal(node)
	// if err != nil {
	// 	return err
	// }
	//
	// newData, err := json.Marshal(nodeCopy)
	// if err != nil {
	// 	return err
	// }

	// TODO: use Patch instead of the Update (The patch is not currently mocked so for testing purpose sticking with the Update for now)
	// See https://github.com/kubernetes/client-go/issues/364
	// patch, err := strategicpatch.CreateTwoWayMergePatch(
	// 	oldData, newData, v1.Node{},
	// )
	//
	// fmt.Printf("patch: %#v\n", string(patch))
	//
	// if err != nil {
	// 	utilruntime.HandleError(fmt.Errorf("Unable to create patch for node %q: %v", node.ObjectMeta.Name, err))
	// 	return nil
	// }
	//
	// n, err := c.kubeclientset.Core().Nodes().Patch(
	// 	node.ObjectMeta.Name, types.StrategicMergePatchType, patch,
	// )

	n, err := c.kubeclientset.Core().Nodes().Update(nodeCopy)

	fmt.Printf("n: %#v\nerr: %v\n\n", n, err)

	return nil
}

func (c *Controller) restartNode(name string) {
	// 1. mark a node unschedulable
	// 1. drain a node
	// 1. restart the node instance through a provider
	// 1. mark a node schedulable

}

func (c *Controller) removeNode(name string) {
	// 1. make a node unschedulable
	// 1. drain a node
	// 1. remove instance of the node from a provider
	// 1. remove the node from the apiserver
}

func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	glog.Info("Starting node auto-repair controller")

	// Wait for the caches to be synced before starting workers
	glog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.nodeInformerSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	glog.Info("Starting workers")

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	glog.Info("Started workers")
	<-stopCh
	glog.Info("Shutting down workers")

	return nil
}

type nodeItem struct {
	nodeReadyCondition v1types.NodeCondition
}

func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		glog.V(4).Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}
	glog.V(4).Infof("Processing object: %s", object.GetName())
	node, err := c.nodeInformerLister.Get(object.GetName())
	if err != nil {
		glog.V(4).Infof("ignoring node '%s'", object.GetName())
		return
	}

	c.enqueueNode(node)
}

func (c *Controller) enqueueNode(node *v1.Node) {
	fmt.Printf("Enqueueing %v\n", node.ObjectMeta.Name)

	for _, condition := range node.Status.Conditions {
		if condition.Type == v1types.NodeReady {
			if condition.Status == v1types.ConditionFalse {
				// How much time to wait: MIN_UNREADY_DELAY - (now - LastTransitionTime)
				// If it is negative, don't wait
				// The expression can be written this way as well:
				// - (now - (LastTransitionTime + MIN_UNREADY_DELAY))
				waitFor := -1 * (time.Now().Sub(condition.LastTransitionTime.Time.Add(MIN_UNREADY_DELAY)))
				if waitFor < 0 {
					waitFor = 0
				}
				fmt.Printf("UnreadyFor: %v, waitFor: %v\n", time.Now().Sub(condition.LastTransitionTime.Time).String(), waitFor.String())
				c.workqueue.AddAfter(node.ObjectMeta.Name, waitFor)
			}
		}
	}

	glog.Infof("Node %v has no Ready status, ignoring", node.ObjectMeta.Name)
}

func (c *Controller) processNodes() error {
	nodes, err := c.nodeInformerLister.List(labels.Everything())
	if err != nil {
		return err
	}

	unreadyNodes := make(map[string]nodeItem)
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
			unreadyNodes[node.ObjectMeta.Name] = nodeItem{
				nodeReadyCondition: NodeReadyCondition,
			}
		} else {
			fmt.Printf("Node %q Ready\n", node.ObjectMeta.Name)
		}
	}

	// TODO(jchaloup): sort the list of nodes by the lastTransitionTime in ascending manner
	for name, node := range unreadyNodes {
		fmt.Printf("LastTransitionTime: %v\n", node.nodeReadyCondition.LastTransitionTime.Time)
		if node.nodeReadyCondition.LastTransitionTime.Time.Add(10 * time.Minute).Before(time.Now()) {
			fmt.Printf("Node %q UnReady for at least 10 minutes\n", name)
		} else {
			fmt.Printf("Node %q UnReady for less than 10 minutes\n", name)
		}
	}

	return nil
}

func makeNode(nodeName string, unreadyDuration time.Duration) *v1.Node {
	return &v1.Node{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Node",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:                       nodeName,
			GenerateName:               "",
			Namespace:                  "",
			SelfLink:                   fmt.Sprintf("/api/v1/nodes/%v", nodeName),
			UID:                        "9a5e86b1-fa09-11e7-9683-507b9deefa09",
			ResourceVersion:            "22948",
			Generation:                 0,
			CreationTimestamp:          metav1.Time{Time: time.Now().Add(-5 * time.Second)},
			DeletionTimestamp:          (*metav1.Time)(nil),
			DeletionGracePeriodSeconds: (*int64)(nil),
			Labels:          map[string]string{"beta.kubernetes.io/arch": "amd64", "beta.kubernetes.io/os": "linux", "kubernetes.io/hostname": nodeName},
			Annotations:     map[string]string{"node.alpha.kubernetes.io/ttl": "0", "volumes.kubernetes.io/controller-managed-attach-detach": "true", "volumes.kubernetes.io/keep-terminated-pod-volumes": "true"},
			OwnerReferences: []metav1.OwnerReference(nil),
			Initializers:    (*metav1.Initializers)(nil),
			Finalizers:      []string(nil),
			ClusterName:     "",
		},
		Spec: v1.NodeSpec{
			PodCIDR:       "",
			ExternalID:    nodeName,
			ProviderID:    "",
			Unschedulable: false,
			Taints:        []v1.Taint(nil)},
		Status: v1.NodeStatus{
			Capacity:    v1.ResourceList{},
			Allocatable: v1.ResourceList{},
			Phase:       "",
			Conditions: []v1.NodeCondition{
				v1.NodeCondition{
					Type:               "OutOfDisk",
					Status:             "False",
					LastHeartbeatTime:  metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					Reason:             "KubeletHasSufficientDisk",
					Message:            "kubelet has sufficient disk space available",
				},
				v1.NodeCondition{
					Type:               "MemoryPressure",
					Status:             "False",
					LastHeartbeatTime:  metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					Reason:             "KubeletHasSufficientMemory",
					Message:            "kubelet has sufficient memory available"},
				v1.NodeCondition{
					Type:               "DiskPressure",
					Status:             "False",
					LastHeartbeatTime:  metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					Reason:             "KubeletHasNoDiskPressure",
					Message:            "kubelet has no disk pressure",
				}, v1.NodeCondition{
					Type:               "Ready",
					Status:             v1.ConditionFalse,
					LastHeartbeatTime:  metav1.Time{Time: time.Now().Add(-5 * time.Second)},
					LastTransitionTime: metav1.Time{Time: time.Now().Add(-1 * unreadyDuration)},
					Reason:             "KubeletReady", Message: "kubelet is posting ready status"},
			},
			Addresses: []v1.NodeAddress{
				v1.NodeAddress{Type: "InternalIP", Address: "127.0.0.1"},
				v1.NodeAddress{Type: "Hostname", Address: "127.0.0.1"},
			},
			DaemonEndpoints: v1.NodeDaemonEndpoints{
				KubeletEndpoint: v1.DaemonEndpoint{Port: 10250},
			},
			NodeInfo: v1.NodeSystemInfo{
				MachineID:               "d42b3db62e5c40f0868de0572d3bea4a",
				SystemUUID:              "464BFF4C-28C8-11B2-A85C-EEC74255F33D",
				BootID:                  "bae459a1-f56a-4641-aa90-eb4571a11a4a",
				KernelVersion:           "4.9.8-100.fc24.x86_64",
				OSImage:                 "Fedora 24 (Workstation Edition)",
				ContainerRuntimeVersion: "docker://1.12.5",
				KubeletVersion:          "v1.10.0-alpha.1.648+2f39e8a04550af-dirty",
				KubeProxyVersion:        "v1.10.0-alpha.1.648+2f39e8a04550af-dirty",
				OperatingSystem:         "linux",
				Architecture:            "amd64",
			},
			Images:          []v1.ContainerImage{},
			VolumesInUse:    []v1.UniqueVolumeName(nil),
			VolumesAttached: []v1.AttachedVolume(nil),
		},
	}
}

func main() {

	if false {
		config, err := clientcmd.BuildConfigFromFlags("", "/var/run/kubernetes/admin.kubeconfig")
		if err != nil {
			panic(err.Error())
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			panic(err.Error())
		}

		fmt.Printf("clientset: %#v\n", clientset)
	}

	fakeCS := fake.NewSimpleClientset(
		runtime.Object(makeNode("node1", 11*time.Minute)),
		runtime.Object(makeNode("node2", 12*time.Minute)),
		runtime.Object(makeNode("node3", 13*time.Minute)),
	)

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(fakeCS, time.Second*30)

	stopCh := make(chan struct{})
	c := NewController(fakeCS, kubeInformerFactory)
	go c.Run(1, stopCh)

	go kubeInformerFactory.Start(stopCh)
	time.Sleep(2 * time.Minute)
	close(stopCh)

}
