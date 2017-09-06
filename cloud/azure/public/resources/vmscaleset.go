// Copyright © 2017 The Kubicorn Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package resources

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/arm/compute"
	"github.com/kris-nova/kubicorn/apis/cluster"
	"github.com/kris-nova/kubicorn/cloud"
	"github.com/kris-nova/kubicorn/cutil/azuremaps"
	"github.com/kris-nova/kubicorn/cutil/compare"
	"github.com/kris-nova/kubicorn/cutil/defaults"
	"github.com/kris-nova/kubicorn/cutil/logger"
)

var _ cloud.Resource = &VMScaleSet{}

type VMScaleSet struct {
	Shared
	ServerPool *cluster.ServerPool
	Image      string
}

func (r *VMScaleSet) Actual(immutable *cluster.Cluster) (*cluster.Cluster, cloud.Resource, error) {
	logger.Debug("vmscaleset.Actual")

	newResource := &VMScaleSet{
		Shared: Shared{
			Name:       r.Name,
			Tags:       r.Tags,
			Identifier: r.ServerPool.Identifier,
		},
	}

	if r.ServerPool.Identifier != "" {
		vss, err := Sdk.Compute.Get(immutable.Name, r.ServerPool.Name)
		if err != nil {
			return nil, nil, err
		}
		newResource.Identifier = *vss.ID
		// Todo (@kris-nova) set Image here
	}

	newCluster := r.immutableRender(newResource, immutable)
	return newCluster, newResource, nil
}

func (r *VMScaleSet) Expected(immutable *cluster.Cluster) (*cluster.Cluster, cloud.Resource, error) {
	logger.Debug("vmscaleset.Expected")
	newResource := &VMScaleSet{
		Shared: Shared{
			Name:       r.Name,
			Tags:       r.Tags,
			Identifier: r.ServerPool.Identifier,
		},
		Image: r.ServerPool.Image,
	}
	newCluster := r.immutableRender(newResource, immutable)
	return newCluster, newResource, nil
}

func (r *VMScaleSet) Apply(actual, expected cloud.Resource, immutable *cluster.Cluster) (*cluster.Cluster, cloud.Resource, error) {
	logger.Debug("vmscaleset.Apply")
	applyResource := expected.(*VMScaleSet)
	isEqual, err := compare.IsEqual(actual.(*VMScaleSet), expected.(*VMScaleSet))
	if err != nil {
		return nil, nil, err
	}
	if isEqual {
		return immutable, applyResource, nil
	}

	if r.ServerPool.Type == cluster.ServerPoolTypeMaster {

		// -------------------------------------------------------------------------------------
		// IP Configs
		var ipConfigsToAdd []compute.VirtualMachineScaleSetIPConfiguration
		for _, serverPool := range immutable.ServerPools {
			if serverPool.Type == cluster.ServerPoolTypeMaster {
				for _, subnet := range serverPool.Subnets {
					var backEndPools []compute.SubResource
					for _, id := range subnet.LoadBalancer.BackendIDs {
						backEndPools = append(backEndPools, compute.SubResource{ID: &id})
					}
					var inboundNatPools []compute.SubResource
					for _, id := range subnet.LoadBalancer.NATIDs {
						inboundNatPools = append(inboundNatPools, compute.SubResource{ID: &id})
					}

					newIpConfig := compute.VirtualMachineScaleSetIPConfiguration{
						VirtualMachineScaleSetIPConfigurationProperties: &compute.VirtualMachineScaleSetIPConfigurationProperties{
							Subnet: &compute.APIEntityReference{
								ID: s(subnet.Identifier),
							},
							LoadBalancerBackendAddressPools: &backEndPools,
							LoadBalancerInboundNatPools:     &inboundNatPools,
						},
						Name: &serverPool.Name,
					}
					ipConfigsToAdd = append(ipConfigsToAdd, newIpConfig)
				}
			}
		}
		imageRef, err := azuremaps.GetImageReferenceFromImage(r.ServerPool.Image)
		if err != nil {
			return nil, nil, err
		}
		parameters := compute.VirtualMachineScaleSet{
			Location: &immutable.Location,
			VirtualMachineScaleSetProperties: &compute.VirtualMachineScaleSetProperties{
				VirtualMachineProfile: &compute.VirtualMachineScaleSetVMProfile{
					StorageProfile: &compute.VirtualMachineScaleSetStorageProfile{
						OsDisk: &compute.VirtualMachineScaleSetOSDisk{
							OsType:       compute.Linux,
							CreateOption: compute.FromImage,
						},
						ImageReference: imageRef,
					},
					OsProfile: &compute.VirtualMachineScaleSetOSProfile{},
					NetworkProfile: &compute.VirtualMachineScaleSetNetworkProfile{
						NetworkInterfaceConfigurations: &[]compute.VirtualMachineScaleSetNetworkConfiguration{
							{
								VirtualMachineScaleSetNetworkConfigurationProperties: &compute.VirtualMachineScaleSetNetworkConfigurationProperties{
									IPConfigurations: &ipConfigsToAdd,
									Primary:          b(true),
								},
							},
						},
					},
				},
				UpgradePolicy: &compute.UpgradePolicy{
					Mode: compute.Automatic,
				},
			},
			Sku: &compute.Sku{
				Name:     s(r.ServerPool.Size),
				Tier:     s(azuremaps.GetTierFromSize(r.ServerPool.Size)),
				Capacity: i64(int64(r.ServerPool.MaxCount)),
			},
			Type: s(""),
			Plan: &compute.Plan{
				Name:          s(""),
				Product:       s(""),
				PromotionCode: s(""),
				Publisher:     s(""),
			},
		}

		vmssch, errch := Sdk.Compute.CreateOrUpdate(immutable.Name, applyResource.Name, parameters, make(chan struct{}))
		vmss := <-vmssch
		err = <-errch
		if err != nil {
			return nil, nil, err
		}
		fmt.Println(vmss)
	}

	newResource := &VMScaleSet{
		Shared: Shared{
			Name:       r.Name,
			Tags:       r.Tags,
			Identifier: r.ServerPool.Identifier,
		},
	}
	newCluster := r.immutableRender(newResource, immutable)
	return newCluster, newResource, nil
}
func (r *VMScaleSet) Delete(actual cloud.Resource, immutable *cluster.Cluster) (*cluster.Cluster, cloud.Resource, error) {
	logger.Debug("vmscaleset.Delete")
	deleteResource := actual.(*VMScaleSet)
	if deleteResource.Identifier == "" {
		return nil, nil, fmt.Errorf("Unable to delete VPC resource without ID [%s]", deleteResource.Name)
	}
	_, errch := Sdk.Compute.Delete(immutable.Name, deleteResource.Name, make(chan struct{}))
	err := <-errch
	if err != nil {
		return nil, nil, err
	}
	newResource := &VMScaleSet{
		Shared: Shared{
			Name:       r.Name,
			Tags:       r.Tags,
			Identifier: "",
		},
	}
	newCluster := r.immutableRender(newResource, immutable)
	return newCluster, newResource, nil
}

func (r *VMScaleSet) immutableRender(newResource cloud.Resource, inaccurateCluster *cluster.Cluster) *cluster.Cluster {
	logger.Debug("vmscaleset.Render")
	newCluster := defaults.NewClusterDefaults(inaccurateCluster)
	return newCluster
}
