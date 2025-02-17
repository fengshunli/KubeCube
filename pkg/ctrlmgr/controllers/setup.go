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

package controllers

import (
	"errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kubecube-io/kubecube/pkg/clog"
	"github.com/kubecube-io/kubecube/pkg/ctrlmgr/controllers/binding"
	cluster "github.com/kubecube-io/kubecube/pkg/ctrlmgr/controllers/cluster"
	"github.com/kubecube-io/kubecube/pkg/ctrlmgr/controllers/quota"
	user "github.com/kubecube-io/kubecube/pkg/ctrlmgr/controllers/user"
)

// todo: change set func if need

var setupFns []func(manager manager.Manager) error

func init() {
	// setup controllers
	setupFns = append(setupFns, cluster.SetupWithManager)
	setupFns = append(setupFns, user.SetupWithManager)
	setupFns = append(setupFns, quota.SetupWithManager)
	setupFns = append(setupFns, binding.SetupClusterRoleBindingReconcilerWithManager)
	setupFns = append(setupFns, binding.SetupRoleBindingReconcilerWithManager)
}

// SetupWithManager set up controllers into manager
func SetupWithManager(m manager.Manager) error {
	for _, f := range setupFns {
		if err := f(m); err != nil {
			var kindMatchErr *meta.NoKindMatchError
			if errors.As(err, &kindMatchErr) {
				clog.Warn("CRD %v is not installed, its controller will dry run!", kindMatchErr.GroupKind)
				continue
			}
			return err
		}
	}
	return nil
}
