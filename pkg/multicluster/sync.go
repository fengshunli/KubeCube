/*
Copyright 2021 KubeCube Authors

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

package multicluster

import (
	"context"
	"fmt"
	"github.com/kubecube-io/kubecube/pkg/utils/informer"

	clusterv1 "github.com/kubecube-io/kubecube/pkg/apis/cluster/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubecube-io/kubecube/pkg/apis"
	"github.com/kubecube-io/kubecube/pkg/clog"
	"github.com/kubecube-io/kubecube/pkg/utils/keys"
	"github.com/kubecube-io/kubecube/pkg/utils/worker"
)

// SyncMgr only running when process as subsidiary
type SyncMgr struct {
	cache       cache.Cache
	Informer    cache.Informer
	Worker      worker.Interface
	isWithScout bool
}

func NewSyncMgr(config *rest.Config, isWithScout bool) (*SyncMgr, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(apis.AddToScheme(scheme))

	c, err := cache.New(config, cache.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}

	cluster := clusterv1.Cluster{}
	im, err := c.GetInformer(context.Background(), &cluster)
	if err != nil {
		return nil, err
	}

	return &SyncMgr{cache: c, Informer: im, isWithScout: isWithScout}, nil
}

func NewSyncMgrWithDefaultSetting(config *rest.Config, isWithScout bool) (*SyncMgr, error) {
	m, err := NewSyncMgr(config, isWithScout)
	if err != nil {
		return nil, err
	}

	m.Informer.AddEventHandler(informer.NewHandlerOnEvents(m.OnClusterAdd, m.OnClusterUpdate, m.OnClusterDelete))
	m.Worker = worker.New("cluster", 0, ClusterWideKeyFunc, m.ReconcileCluster)
	return m, nil
}

// Start keep sync cluster change by informer
func (m *SyncMgr) Start(ctx context.Context) error {
	stopCh := ctx.Done()

	m.Worker.Run(1, stopCh)

	go func() {
		err := m.cache.Start(ctx)
		if err != nil {
			clog.Fatal("start cluster sync cache failed")
		}
		clog.Info("sync manager exit")
	}()

	if !m.cache.WaitForCacheSync(ctx) {
		return fmt.Errorf("cluster sync cache can not wait for sync")
	}

	// list all clusters and process at first
	clusters := clusterv1.ClusterList{}
	if err := m.cache.List(ctx, &clusters); err != nil {
		return err
	}

	for _, cluster := range clusters.Items {
		obj := cluster
		key, err := ClusterWideKeyFunc(&obj)
		if err != nil {
			return err
		}
		if err = m.ReconcileCluster(key); err != nil {
			return err
		}
	}

	clog.Info("sync manager is running")
	return nil
}

// ClusterWideKeyFunc generates a ClusterWideKey for object.
func ClusterWideKeyFunc(obj interface{}) (worker.QueueKey, error) {
	return keys.ClusterWideKeyFunc(obj)
}

func (m *SyncMgr) OnClusterAdd(obj interface{}) {
	key, err := ClusterWideKeyFunc(obj)
	if err != nil {
		return
	}

	m.Worker.AddRateLimited(key)
}

func (m *SyncMgr) OnClusterDelete(obj interface{}) {
	m.OnClusterAdd(obj)
}

func (m *SyncMgr) OnClusterUpdate(oldObj, newObj interface{}) {
	oldCluster := oldObj.(*clusterv1.Cluster)
	newCluster := newObj.(*clusterv1.Cluster)
	initFailedState, ProcessingState := clusterv1.ClusterInitFailed, clusterv1.ClusterProcessing
	if oldCluster.Status.State == &initFailedState &&
		newCluster.Status.State == &ProcessingState {
		key, err := ClusterWideKeyFunc(newObj)
		if err != nil {
			return
		}

		m.Worker.AddRateLimited(key)
	}
}

// ReconcileCluster sync cluster during multi KubeCube instance
func (m *SyncMgr) ReconcileCluster(key worker.QueueKey) error {
	ckey, ok := key.(keys.ClusterWideKey)
	if !ok {
		clog.Error("found invalid key when reconciling resource cluster")
		return fmt.Errorf("invalid key")
	}

	cluster := &clusterv1.Cluster{}
	err := m.cache.Get(context.Background(), client.ObjectKey{Name: ckey.Name}, cluster)
	if err != nil {
		// delete internal cluster if cluster was deleted
		if errors.IsNotFound(err) {
			err = ManagerImpl.Del(cluster.Name)
			if err != nil {
				clog.Warn(err.Error())
			}
			return nil
		}
		return err
	}

	if m.isWithScout {
		err = AddInternalClusterWithScout(*cluster)
		if err != nil {
			clog.Error("add internal cluster %v failed: %v", cluster.Name, err)
			return err
		}

		// start to scout for warden
		err = ManagerImpl.ScoutFor(context.Background(), cluster.Name)
		if err != nil {
			clog.Error("scout for %v warden failed: %v", cluster.Name, err)
			return err
		}
	} else {
		err = AddInternalCluster(*cluster)
		if err != nil {
			clog.Error("add internal cluster %v failed: %v", cluster.Name, err)
			return err
		}
	}

	return nil
}
