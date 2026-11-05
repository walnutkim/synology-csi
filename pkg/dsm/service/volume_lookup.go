/*
 * Copyright 2021 Synology Inc.
 */

package service

import (
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/SynologyOpenSource/synology-csi/pkg/models"
	"github.com/SynologyOpenSource/synology-csi/pkg/utils"
)

// The lookups below find one volume by ID without listing every volume.
//
// ListVolumes calls LunGet once for every LUN mapped on every DSM and loads the
// NFS privilege of every driver share, so a GetVolume built on it costs as many
// DSM API calls as there are volumes. Node RPCs call GetVolume each time
// (stage, publish, unstage, expand and the periodic NodeGetVolumeStats). With
// about a hundred volumes on two DSMs we measured one to two minutes per call,
// and the stats calls alone kept the DSM busy around the clock.
//
// Each lookup returns exactly what ListVolumes would return for that ID, or nil
// when it cannot settle the lookup. GetVolume then falls back to ListVolumes, so
// a volume these lookups miss is still found the old way.

// findISCSIVolume finds an iSCSI volume by its LUN UUID with one LunGet per DSM
// and one TargetList on the DSM that holds the LUN.
func (service *DsmService) findISCSIVolume(volId string) *models.K8sVolumeRespSpec {
	for _, dsm := range service.dsms {
		lun, err := dsm.LunGet(volId)
		if err != nil {
			// Expected on a DSM that does not hold this LUN and for share or NVMe IDs.
			log.Debugf("[%s] LunGet(%s) found no LUN: %v", dsm.Ip, volId, err)
			continue
		}
		// listISCSIVolumes skips LUNs the driver did not create.
		if lun.Uuid != volId || !strings.HasPrefix(lun.Name, models.DevicePrefix) {
			continue
		}

		targets, err := dsm.TargetList()
		if err != nil {
			log.Errorf("[%s] Failed to list targets: %v", dsm.Ip, err)
			continue
		}
		// Like listISCSIVolumes, pair the LUN with the first target that maps it.
		for _, target := range targets {
			for _, mapping := range target.MappedLuns {
				if mapping.LunUuid == volId {
					return DsmLunToK8sVolume(dsm.Ip, lun, target)
				}
			}
		}
	}
	return nil
}

// findShareVolume finds an SMB or NFS volume by its share UUID. It lists the
// shares once per DSM and loads the NFS privilege only of the share that matches.
func (service *DsmService) findShareVolume(volId string) *models.K8sVolumeRespSpec {
	for _, dsm := range service.dsms {
		if dsm.IsUC() {
			continue
		}

		shares, err := dsm.ShareList()
		if err != nil {
			log.Errorf("[%s] Failed to list shares: %v", dsm.Ip, err)
			continue
		}

		for _, share := range shares {
			if share.Uuid != volId || !strings.HasPrefix(share.Name, models.SharePrefix) {
				continue
			}
			// As in listSMBorNFSVolumes, a share with an NFS rule is an NFS volume.
			sharePrivilege, err := dsm.ShareNfsPrivilegeLoad(share.Name)
			if err != nil {
				log.Errorf("[%s] Failed to load share nfs privilege: %v", dsm.Ip, err)
				return nil
			}
			if len(sharePrivilege.Rule) > 0 {
				return DsmShareToK8sVolume(dsm.Ip, share, utils.ProtocolNfs)
			}
			return DsmShareToK8sVolume(dsm.Ip, share, utils.ProtocolSmb)
		}
	}
	return nil
}
