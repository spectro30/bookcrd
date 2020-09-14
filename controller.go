package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/golang/glog"
	appsv1 "k8s.io/api/apps/v1"
	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	kutil "kmodules.xyz/client-go"
	"time"

	clusterv1alpha1 "github.com/spectro30/bookcrd/apis/cluster/v1alpha1"
	clientset "github.com/spectro30/bookcrd/client/clientset/versioned"
	samplescheme "github.com/spectro30/bookcrd/client/clientset/versioned/scheme"
	clusterInformer "github.com/spectro30/bookcrd/client/informers/externalversions/cluster/v1alpha1"
	clusterLister "github.com/spectro30/bookcrd/client/listers/cluster/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
	kerr "k8s.io/apimachinery/pkg/api/errors"
)

const controllerAgentName = "cluster-controller"

type controller struct {
	//k8s clientset
	kubeClientSet kubernetes.Interface
	//this one is for cluster
	clusterClientSet clientset.Interface

	deploymentsListener appslisters.DeploymentLister
	deploymentsSynced   cache.InformerSynced

	podsListener corelisters.PodLister
	podsSynced   cache.InformerSynced

	clusterLister clusterLister.ClusterLister
	clusterSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

func newController(kubeClientSet kubernetes.Interface,
	clusterClientSet clientset.Interface,
	deploymentInformer appsinformers.DeploymentInformer,
	podInformer coreinformers.PodInformer,
	clusterInformer clusterInformer.ClusterInformer) *controller {
	// Create event broadcaster
	// Add sample-controller types to the default Kubernetes Scheme so Events can be
	// logged for sample-controller types.
	utilruntime.Must(samplescheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClientSet.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	fmt.Println("Hello There")
	controller := &controller{
		kubeClientSet:    kubeClientSet,
		clusterClientSet: clusterClientSet,

		deploymentsListener: deploymentInformer.Lister(),
		deploymentsSynced:   deploymentInformer.Informer().HasSynced,

		podsListener: podInformer.Lister(),
		podsSynced:   podInformer.Informer().HasSynced,

		clusterLister: clusterInformer.Lister(),
		clusterSynced: clusterInformer.Informer().HasSynced,
		workqueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Clusters"),
		recorder:      recorder,
	}

	deploymentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(oldObj, newObj interface{}) {
			controller.handleObject(newObj)
		},
		DeleteFunc: controller.handleObject,
	})
	//podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
	//	AddFunc: ,
	//	UpdateFunc: ,
	//	DeleteFunc: ,
	//})
	clusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if o, ok := obj.(*clusterv1alpha1.Cluster); ok && o.GetGeneration() > o.Status.ObservedGeneration {
				controller.enqueue(obj)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			controller.enqueue(newObj)
		},
		DeleteFunc: controller.enqueue,
	})

	return controller

}
func (c *controller) Run(threadiness int, stopch <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting cluster controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")

	if ok := cache.WaitForCacheSync(stopch, c.deploymentsSynced, c.podsSynced, c.clusterSynced); !ok {
		klog.Info("can't sync cache  properly")
		return fmt.Errorf("failed to wait for cache sync")
	}

	//now i need to start workers

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopch)
	}
	klog.Info("Started workers to process the queue")
	<-stopch
	klog.Info("Shutting down workers program has been terminated")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *controller) processNextWorkItem() bool {
	obj, shutDown := c.workqueue.Get()
	if shutDown {
		return false
	}
	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {

			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}

		// now need to pass the key to syncHandler
		if err := c.syncHandler(key); err != nil {
			// put it back again in the queue
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil

		//why put object here????
	}(obj)
	if err != nil {
		utilruntime.HandleError(err)
	}
	return true
}


func CreateOrPatchDeployment(ctx context.Context, c kubernetes.Interface, meta metav1.ObjectMeta, transform func(*apps.Deployment) *apps.Deployment, opts metav1.PatchOptions) (*apps.Deployment, kutil.VerbType, error) {
	cur, err := c.AppsV1().Deployments(meta.Namespace).Get(ctx, meta.Name, metav1.GetOptions{})
	if kerr.IsNotFound(err) {
		glog.V(3).Infof("Creating Deployment %s/%s.", meta.Namespace, meta.Name)
		out, err := c.AppsV1().Deployments(meta.Namespace).Create(ctx, transform(&apps.Deployment{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: apps.SchemeGroupVersion.String(),
			},
			ObjectMeta: meta,
		}), metav1.CreateOptions{
			DryRun:       opts.DryRun,
			FieldManager: opts.FieldManager,
		})
		return out, kutil.VerbCreated, err
	} else if err != nil {
		return nil, kutil.VerbUnchanged, err
	}
	return PatchDeployment(ctx, c, cur, transform, opts)
}

func PatchDeployment(ctx context.Context, c kubernetes.Interface, cur *apps.Deployment, transform func(*apps.Deployment) *apps.Deployment, opts metav1.PatchOptions) (*apps.Deployment, kutil.VerbType, error) {
	return PatchDeploymentObject(ctx, c, cur, transform(cur.DeepCopy()), opts)
}

func PatchDeploymentObject(ctx context.Context, c kubernetes.Interface, cur, mod *apps.Deployment, opts metav1.PatchOptions) (*apps.Deployment, kutil.VerbType, error) {
	curJson, err := json.Marshal(cur)
	if err != nil {
		return nil, kutil.VerbUnchanged, err
	}

	modJson, err := json.Marshal(mod)
	if err != nil {
		return nil, kutil.VerbUnchanged, err
	}

	patch, err := strategicpatch.CreateTwoWayMergePatch(curJson, modJson, apps.Deployment{})
	if err != nil {
		return nil, kutil.VerbUnchanged, err
	}
	if len(patch) == 0 || string(patch) == "{}" {
		return cur, kutil.VerbUnchanged, nil
	}
	glog.V(3).Infof("Patching Deployment %s/%s with %s.", cur.Namespace, cur.Name, string(patch))
	out, err := c.AppsV1().Deployments(cur.Namespace).Patch(ctx, cur.Name, types.StrategicMergePatchType, patch, opts)
	return out, kutil.VerbPatched, err
}

func (c *controller) syncHandler(key string) error {

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("can't retrieve namespace. error : ", err.Error()))
		return nil
	}

	cluster, err := c.clusterLister.Clusters(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			//maybe the resource is no longer available

			utilruntime.HandleError(fmt.Errorf("the item can't be finded maybe it's deleted", err.Error()))
			return nil
		}
		return err

	}
	deploy := newDeployment(cluster)
	c.kubeClientSet.AppsV1().Deployments("").Create(context.TODO(), deploy, metav1.CreateOptions{})

	bookDeploymentName := cluster.Spec.DeploymentName
	if bookDeploymentName == "" {
		// We choose to absorb the error here as the worker would requeue the
		// resource otherwise. Instead, the next time the resource is updated
		// the resource will be queued again.
		utilruntime.HandleError(fmt.Errorf("%s: deployment name must be specified", key))
		return nil
	}

	bookDeployment,_, err := CreateOrPatchDeployment(context.TODO(), c.kubeClientSet, deploy.ObjectMeta, func(deployment *apps.Deployment) *apps.Deployment {
		if cluster.Spec.ReplicaCount != nil {
			deployment.Spec.Replicas = cluster.Spec.ReplicaCount
		}
		return deployment
	}, metav1.PatchOptions{})

	if err != nil {
		return err
	}

	// Finally, we update the status block of the Foo resource to reflect the
	// current state of the world
	err = c.updateClusterStatus(cluster, bookDeployment)
	if err != nil {
		return err
	}

	c.recorder.Event(cluster, corev1.EventTypeNormal, "SuccessSynced", "Message Resource Synced")
	return nil

}

func (c *controller) updateClusterStatus(cluster *clusterv1alpha1.Cluster,
	bookdeployment *appsv1.Deployment) error {
	clusterCopy := cluster.DeepCopy()
	if cluster.DeletionTimestamp == nil {
		clusterCopy.Status.CurrentReplica = bookdeployment.Status.AvailableReplicas
	}
	clusterCopy.Status.ObservedGeneration = cluster.ObjectMeta.GetGeneration()
	//clusterCopy.Status.ObservedGeneration =
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the Foo resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := c.clusterClientSet.ClusterV1alpha1().Clusters(cluster.Namespace).Update(context.TODO(), clusterCopy, metav1.UpdateOptions{})
	return err
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the Foo resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that Foo resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *controller) handleObject(obj interface{}) {
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
		klog.V(4).Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}
	klog.V(4).Infof("Processing object: %s", object.GetName())
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a Foo, we should not do anything more
		// with it.
		if ownerRef.Kind != "cluster" {
			return
		}

		cluster, err := c.clusterLister.Clusters(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			klog.V(4).Infof("ignoring orphaned object '%s' of cluster '%s'", object.GetSelfLink(), ownerRef.Name)
			return
		}

		c.enqueue(cluster)
		return
	}
}
func (c *controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)

}

// newDeployment creates a new Deployment for a Foo resource. It also sets
// the appropriate OwnerReferences on the resource so handleObject can discover
// the Foo resource that 'owns' it.
func newDeployment(cluster *clusterv1alpha1.Cluster) *appsv1.Deployment {
	labels := map[string]string{
		"app":        "book-server",
		"controller": cluster.Name,
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, clusterv1alpha1.SchemeGroupVersion.WithKind("Cluster")),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: cluster.Spec.ReplicaCount,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "book-server",
							Image: "spectro30/bookapp:latest",
						},
					},
				},
			},
		},
	}
}