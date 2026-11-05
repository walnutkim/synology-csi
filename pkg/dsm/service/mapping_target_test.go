/*
 * Copyright 2021 Synology Inc.
 */

package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SynologyOpenSource/synology-csi/pkg/dsm/webapi"
	"github.com/SynologyOpenSource/synology-csi/pkg/models"
)

// fakeTargetDsm answers the target calls createMappingTarget makes. Like DSM,
// mapping a LUN that a target already maps adds another mapping to it.
type fakeTargetDsm struct {
	mu      sync.Mutex
	targets []webapi.TargetInfo
	mapped  int
}

func (f *fakeTargetDsm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	query := r.URL.Query()
	switch query.Get("api") + "." + query.Get("method") {
	case "SYNO.Core.ISCSI.Target.create":
		name := query.Get("name")
		if f.target(name) != nil {
			fail(w, 18990744) // Duplicated target name
			return
		}
		f.targets = append(f.targets, webapi.TargetInfo{Name: name, Iqn: query.Get("iqn"), TargetId: len(f.targets) + 1})
		reply(w, map[string]int{"target_id": len(f.targets)})
	case "SYNO.Core.ISCSI.Target.get":
		// The driver passes the target name as target_id.
		key, _ := strconv.Unquote(query.Get("target_id"))
		target := f.target(key)
		if target == nil {
			fail(w, 18990710)
			return
		}
		reply(w, map[string]interface{}{"target": target})
	case "SYNO.Core.ISCSI.LUN.map_target":
		uuid, _ := strconv.Unquote(query.Get("uuid"))
		var ids []int
		_ = json.Unmarshal([]byte(query.Get("target_ids")), &ids)
		for _, id := range ids {
			for i := range f.targets {
				if f.targets[i].TargetId == id {
					f.targets[i].MappedLuns = append(f.targets[i].MappedLuns,
						webapi.MappedLun{LunUuid: uuid, MappingIndex: len(f.targets[i].MappedLuns) + 1})
				}
			}
		}
		f.mapped++
		reply(w, map[string]interface{}{})
	default:
		fail(w, 102)
	}
}

func (f *fakeTargetDsm) target(key string) *webapi.TargetInfo {
	for i := range f.targets {
		if f.targets[i].Name == key || strconv.Itoa(f.targets[i].TargetId) == key {
			return &f.targets[i]
		}
	}
	return nil
}

func newTargetTestDsm(t *testing.T, fake *fakeTargetDsm) *webapi.DSM {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return &webapi.DSM{Ip: "127.0.0.1", Port: port}
}

func TestCreateMappingTargetMapsTheLUNOnce(t *testing.T) {
	fake := &fakeTargetDsm{}
	dsm := newTargetTestDsm(t, fake)
	service := NewDsmService()
	spec := &models.CreateK8sVolumeSpec{
		K8sVolumeName: "pvc-01",
		TargetName:    models.TargetPrefix + "-pvc-01",
	}

	// The first call creates and maps; the provisioner's retry repeats it.
	for i := 0; i < 2; i++ {
		if _, err := service.createMappingTarget(dsm, spec, "lun-uuid-01"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}

	target := fake.target(spec.TargetName)
	if target == nil {
		t.Fatal("target was not created")
	}
	if fake.mapped != 1 || len(target.MappedLuns) != 1 {
		t.Fatalf("LunMapTarget called %d times, target maps %v; want one mapping", fake.mapped, target.MappedLuns)
	}
}

func TestCreateMappingTargetMapsAnotherLUNOnAnExistingTarget(t *testing.T) {
	fake := &fakeTargetDsm{targets: []webapi.TargetInfo{{
		Name:       models.TargetPrefix + "-pvc-02",
		TargetId:   1,
		MappedLuns: []webapi.MappedLun{{LunUuid: "other-lun", MappingIndex: 1}},
	}}}
	dsm := newTargetTestDsm(t, fake)
	spec := &models.CreateK8sVolumeSpec{K8sVolumeName: "pvc-02", TargetName: models.TargetPrefix + "-pvc-02"}

	if _, err := NewDsmService().createMappingTarget(dsm, spec, "lun-uuid-02"); err != nil {
		t.Fatal(err)
	}

	var uuids []string
	for _, mapping := range fake.target(spec.TargetName).MappedLuns {
		uuids = append(uuids, mapping.LunUuid)
	}
	if strings.Join(uuids, ",") != "other-lun,lun-uuid-02" {
		t.Fatalf("target maps %v, want other-lun and lun-uuid-02", uuids)
	}
}
