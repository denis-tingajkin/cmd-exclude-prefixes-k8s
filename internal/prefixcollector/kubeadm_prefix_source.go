// Copyright (c) 2020 Doc.ai and/or its affiliates.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prefixcollector

import (
	"cmd-exclude-prefixes-k8s/internal/utils"
	"context"
	"github.com/sirupsen/logrus"
	apiV1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta2"
	"strings"
)

const (
	// KubeNamespace is KubeAdm ConfigMap namespace
	KubeNamespace = "kube-system"
	// KubeName is KubeAdm ConfigMap name
	KubeName   = "kubeadm-config"
	bufferSize = 4096
)

// KubeAdmPrefixSource is KubeAdm ConfigMap excluded prefix source
type KubeAdmPrefixSource struct {
	configMapInterface v1.ConfigMapInterface
	prefixes           utils.SynchronizedPrefixesContainer
	ctx                context.Context
	notify             Notifier
}

// Prefixes returns prefixes from source
func (kaps *KubeAdmPrefixSource) Prefixes() []string {
	return kaps.prefixes.GetList()
}

// NewKubeAdmPrefixSource creates KubeAdmPrefixSource
func NewKubeAdmPrefixSource(ctx context.Context, notify Notifier) *KubeAdmPrefixSource {
	clientSet := FromContext(ctx)
	configMapInterface := clientSet.CoreV1().ConfigMaps(KubeNamespace)
	kaps := KubeAdmPrefixSource{
		configMapInterface: configMapInterface,
		ctx:                ctx,
		notify:             notify,
	}

	go kaps.watchKubeAdmConfigMap()
	return &kaps
}

func (kaps *KubeAdmPrefixSource) watchKubeAdmConfigMap() {
	kaps.checkCurrentConfigMap()
	configMapWatch, err := kaps.configMapInterface.Watch(kaps.ctx, metav1.ListOptions{})
	if err != nil {
		logrus.Errorf("Error creating config map watch: %v", err)
		return
	}

	for kaps.ctx.Err() == nil {
		select {
		case event, ok := <-configMapWatch.ResultChan():
			if !ok {
				return
			}

			if event.Type == watch.Error {
				continue
			}

			configMap, ok := event.Object.(*apiV1.ConfigMap)
			if !ok || configMap.Name != KubeName {
				continue
			}

			if event.Type == watch.Deleted {
				kaps.prefixes.SetList([]string{})
				kaps.notify.Broadcast()
				continue
			}

			if err = kaps.setPrefixesFromConfigMap(configMap); err != nil {
				logrus.Error(err)
			}
		case <-kaps.ctx.Done():
		}
	}
}

func (kaps *KubeAdmPrefixSource) checkCurrentConfigMap() {
	configMap, err := kaps.configMapInterface.Get(kaps.ctx, KubeName, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Error getting KubeAdm config map : %v", err)
	}

	if err = kaps.setPrefixesFromConfigMap(configMap); err != nil {
		logrus.Error(err)
	}
}

func (kaps *KubeAdmPrefixSource) setPrefixesFromConfigMap(configMap *apiV1.ConfigMap) error {
	clusterConfiguration := &v1beta2.ClusterConfiguration{}
	err := yaml.NewYAMLOrJSONDecoder(
		strings.NewReader(configMap.Data["ClusterConfiguration"]), bufferSize,
	).Decode(clusterConfiguration)

	if err != nil {
		return err
	}

	podSubnet := clusterConfiguration.Networking.PodSubnet
	serviceSubnet := clusterConfiguration.Networking.ServiceSubnet

	if podSubnet == "" {
		logrus.Error("ClusterConfiguration.Networking.PodSubnet is empty")
	}
	if serviceSubnet == "" {
		logrus.Error("ClusterConfiguration.Networking.ServiceSubnet is empty")
	}

	prefixes := []string{podSubnet, serviceSubnet}

	kaps.prefixes.SetList(prefixes)
	kaps.notify.Broadcast()
	logrus.Infof("Prefixes sent from kubeadm source: %v", prefixes)

	return nil
}
