/*
 * Copyright 2021 Synology Inc.
 */

package driver

import (
	"context"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/SynologyOpenSource/synology-csi/pkg/interfaces"
	"github.com/SynologyOpenSource/synology-csi/pkg/models"
	"github.com/SynologyOpenSource/synology-csi/pkg/utils"
)

// blockingDsmService holds CreateVolume until the test releases it, the way a
// slow DSM keeps the first call running after the provisioner gave up on it.
type blockingDsmService struct {
	interfaces.IDsmService
	entered chan string
	release chan struct{}
}

func (s *blockingDsmService) GetVolumeByName(string) *models.K8sVolumeRespSpec { return nil }

func (s *blockingDsmService) CreateVolume(spec *models.CreateK8sVolumeSpec) (*models.K8sVolumeRespSpec, error) {
	s.entered <- spec.K8sVolumeName
	<-s.release
	return &models.K8sVolumeRespSpec{
		DsmIp:       "127.0.0.1",
		VolumeId:    "lun-uuid-" + spec.K8sVolumeName,
		SizeInBytes: spec.Size,
		Protocol:    spec.Protocol,
	}, nil
}

func newCreateTestServer(dsmService interfaces.IDsmService) *controllerServer {
	d := &Driver{DsmService: dsmService}
	d.addVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
	})
	return NewControllerServer(d)
}

func createRequest(name string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:          name,
		CapacityRange: &csi.CapacityRange{RequiredBytes: utils.UNIT_GB},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		}},
		Parameters: map[string]string{"protocol": utils.ProtocolIscsi},
	}
}

func TestCreateVolumeTurnsAwayARetryWhileTheFirstCallRuns(t *testing.T) {
	dsmService := &blockingDsmService{entered: make(chan string, 3), release: make(chan struct{})}
	released := false
	t.Cleanup(func() {
		if !released {
			close(dsmService.release)
		}
	})
	cs := newCreateTestServer(dsmService)

	first := make(chan error, 1)
	go func() {
		_, err := cs.CreateVolume(context.Background(), createRequest("pvc-01"))
		first <- err
	}()
	<-dsmService.entered

	retry := make(chan error, 1)
	go func() {
		_, err := cs.CreateVolume(context.Background(), createRequest("pvc-01"))
		retry <- err
	}()
	select {
	case err := <-retry:
		if status.Code(err) != codes.Aborted {
			t.Fatalf("retry while the first call runs: got %v, want Aborted", err)
		}
	case <-dsmService.entered:
		t.Fatal("retry reached the DSM while the first call was still running")
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not return")
	}

	// Another volume is not held up by it.
	other := make(chan error, 1)
	go func() {
		_, err := cs.CreateVolume(context.Background(), createRequest("pvc-02"))
		other <- err
	}()
	if name := <-dsmService.entered; name != "pvc-02" {
		t.Fatalf("CreateVolume reached the DSM for %s, want pvc-02", name)
	}

	close(dsmService.release)
	released = true
	if err := <-first; err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := <-other; err != nil {
		t.Fatalf("other volume: %v", err)
	}

	// Once the first call is done, a retry runs again.
	resp, err := cs.CreateVolume(context.Background(), createRequest("pvc-01"))
	if err != nil {
		t.Fatalf("retry after the first call finished: %v", err)
	}
	if resp.GetVolume().GetVolumeId() != "lun-uuid-pvc-01" {
		t.Fatalf("volume id %s", resp.GetVolume().GetVolumeId())
	}
}
