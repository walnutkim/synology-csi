/*
 * Copyright 2021 Synology Inc.
 */

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/SynologyOpenSource/synology-csi/pkg/dsm/webapi"
	"github.com/SynologyOpenSource/synology-csi/pkg/models"
	"github.com/SynologyOpenSource/synology-csi/pkg/utils"
)

// fakeDsm answers the DSM web API calls the volume lookups make and counts them.
type fakeDsm struct {
	luns     []webapi.LunInfo
	targets  []webapi.TargetInfo
	shares   []webapi.ShareInfo
	nfsRules map[string][]webapi.PrivilegeRule
	// Shares whose NFS privilege load fails.
	brokenPrivileges map[string]bool
	// Serve as a UC (unified controller) DSM, which has no shares.
	uc bool

	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeDsm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	call := query.Get("api") + "." + query.Get("method")
	f.mu.Lock()
	f.calls[call]++
	f.mu.Unlock()

	switch call {
	case "SYNO.Core.ISCSI.LUN.get":
		// Like DSM, accept a LUN name as well as a UUID; the driver passes names
		// when it creates volumes.
		key, _ := strconv.Unquote(query.Get("uuid"))
		for _, lun := range f.luns {
			if lun.Uuid == key || lun.Name == key {
				reply(w, map[string]interface{}{"lun": lun})
				return
			}
		}
		fail(w, 18990531) // No such LUN, what DSM returns for an unknown UUID
	case "SYNO.Core.ISCSI.Target.list":
		reply(w, map[string]interface{}{"targets": f.targets})
	case "SYNO.Core.Share.list":
		reply(w, map[string]interface{}{"shares": f.shares})
	case "SYNO.Core.FileServ.NFS.SharePrivilege.load":
		name, _ := strconv.Unquote(query.Get("share_name"))
		if f.brokenPrivileges[name] {
			fail(w, 3300)
			return
		}
		reply(w, map[string]interface{}{"share_name": name, "rule": f.nfsRules[name]})
	default:
		fail(w, 102)
	}
}

func reply(w http.ResponseWriter, data interface{}) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": data})
}

func fail(w http.ResponseWriter, code int) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": map[string]int{"code": code}})
}

func (f *fakeDsm) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = map[string]int{}
}

func (f *fakeDsm) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[call]
}

// newTestService serves each fake DSM on its own port. The hosts differ so that
// the DSMs keep distinct IPs, as AddDsm keys them by IP.
func newTestService(t *testing.T, fakes ...*fakeDsm) *DsmService {
	t.Helper()
	hosts := []string{"127.0.0.1", "localhost"}
	if len(fakes) > len(hosts) {
		t.Fatalf("at most %d fake DSMs", len(hosts))
	}
	service := NewDsmService()
	for i, fake := range fakes {
		fake.resetCalls()
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
		dsm := &webapi.DSM{Ip: hosts[i], Port: port}
		if fake.uc {
			dsm.FirmwareVer = "DSM UC 3.1.4-23602"
		}
		service.dsms[hosts[i]] = dsm
	}
	return service
}

// listedVolume is what GetVolume returned before the direct lookups.
func listedVolume(service *DsmService, volId string) *models.K8sVolumeRespSpec {
	for _, volume := range service.ListVolumes() {
		if volume.VolumeId == volId {
			return volume
		}
	}
	return nil
}

// assertSameAsListing checks that GetVolume returns what the full listing finds.
func assertSameAsListing(t *testing.T, service *DsmService, volId string, fakes ...*fakeDsm) *models.K8sVolumeRespSpec {
	t.Helper()
	want := listedVolume(service, volId)
	for _, fake := range fakes {
		fake.resetCalls()
	}
	got := service.GetVolume(volId)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetVolume(%s) = %+v, want %+v", volId, got, want)
	}
	return got
}

func lunWithTarget(i int) (webapi.LunInfo, webapi.TargetInfo) {
	uuid := fmt.Sprintf("lun-uuid-%02d", i)
	lun := webapi.LunInfo{
		Name:     models.GenBackendName(fmt.Sprintf("pvc-%02d", i)),
		Uuid:     uuid,
		Location: "/volume1",
		Size:     uint64(i+1) << 30,
		Status:   "normal",
	}
	target := webapi.TargetInfo{
		Name:       fmt.Sprintf("target-%02d", i),
		Iqn:        fmt.Sprintf("iqn.2000-01.com.synology:test.pvc-%02d", i),
		TargetId:   i + 1,
		MappedLuns: []webapi.MappedLun{{LunUuid: uuid, MappingIndex: 1}},
	}
	return lun, target
}

func manyLunsDsm(n int) *fakeDsm {
	fake := &fakeDsm{}
	for i := 0; i < n; i++ {
		lun, target := lunWithTarget(i)
		fake.luns = append(fake.luns, lun)
		fake.targets = append(fake.targets, target)
	}
	return fake
}

func TestGetVolumeFindsISCSIVolumeWithoutListing(t *testing.T) {
	fake := manyLunsDsm(30)
	service := newTestService(t, fake)

	got := assertSameAsListing(t, service, "lun-uuid-17", fake)

	if got == nil || got.Protocol != utils.ProtocolIscsi || got.Target.Iqn != "iqn.2000-01.com.synology:test.pvc-17" {
		t.Fatalf("unexpected volume %+v", got)
	}
	if n := fake.count("SYNO.Core.ISCSI.LUN.get"); n != 1 {
		t.Errorf("LunGet called %d times, want 1", n)
	}
	if n := fake.count("SYNO.Core.ISCSI.Target.list"); n != 1 {
		t.Errorf("TargetList called %d times, want 1", n)
	}
	if n := fake.count("SYNO.Core.Share.list"); n != 0 {
		t.Errorf("ShareList called %d times, want 0", n)
	}
}

func TestGetVolumePairsLUNWithFirstMappingTarget(t *testing.T) {
	fake := manyLunsDsm(3)
	// The FIXME in listISCSIVolumes: a LUN mapped to two targets. The listing
	// returns it with the first target in TargetList order.
	fake.targets = append([]webapi.TargetInfo{{
		Name:       "target-shared",
		Iqn:        "iqn.2000-01.com.synology:test.shared",
		TargetId:   99,
		MappedLuns: []webapi.MappedLun{{LunUuid: "lun-uuid-01", MappingIndex: 2}},
	}}, fake.targets...)
	service := newTestService(t, fake)

	got := assertSameAsListing(t, service, "lun-uuid-01", fake)

	if got.Target.Iqn != "iqn.2000-01.com.synology:test.shared" {
		t.Fatalf("paired with target %s, want the first one that maps the LUN", got.Target.Iqn)
	}
}

func TestGetVolumeFindsShareVolumeWithoutLoadingEveryPrivilege(t *testing.T) {
	fake := manyLunsDsm(2)
	for i := 0; i < 10; i++ {
		fake.shares = append(fake.shares, webapi.ShareInfo{
			Name:           models.GenShareName(fmt.Sprintf("pvc-share-%02d", i)),
			VolPath:        "/volume2",
			Uuid:           fmt.Sprintf("share-uuid-%02d", i),
			QuotaValueInMB: int64(i+1) * 1024,
		})
	}
	nfsShare := fake.shares[4].Name
	fake.nfsRules = map[string][]webapi.PrivilegeRule{nfsShare: {{Client: "10.0.0.1", Privilege: "rw"}}}
	service := newTestService(t, fake)

	nfs := assertSameAsListing(t, service, "share-uuid-04", fake)
	if nfs.Protocol != utils.ProtocolNfs {
		t.Fatalf("protocol %s, want nfs", nfs.Protocol)
	}
	if n := fake.count("SYNO.Core.FileServ.NFS.SharePrivilege.load"); n != 1 {
		t.Errorf("ShareNfsPrivilegeLoad called %d times, want 1", n)
	}
	if n := fake.count("SYNO.Core.ISCSI.Target.list"); n != 0 {
		t.Errorf("TargetList called %d times, want 0", n)
	}

	smb := assertSameAsListing(t, service, "share-uuid-07", fake)
	if smb.Protocol != utils.ProtocolSmb {
		t.Fatalf("protocol %s, want smb", smb.Protocol)
	}
}

func TestGetVolumeFindsLUNOnAnotherDsm(t *testing.T) {
	first := manyLunsDsm(2)
	second := &fakeDsm{}
	lun, target := lunWithTarget(7)
	second.luns = []webapi.LunInfo{lun}
	second.targets = []webapi.TargetInfo{target}
	service := newTestService(t, first, second)

	got := assertSameAsListing(t, service, "lun-uuid-07", first, second)

	if got.DsmIp != "localhost" {
		t.Fatalf("found on DSM %s, want localhost", got.DsmIp)
	}
	if n := first.count("SYNO.Core.ISCSI.Target.list"); n != 0 {
		t.Errorf("TargetList called %d times on the DSM without the LUN, want 0", n)
	}
}

func TestGetVolumeFallsBackToListingWhenNotFound(t *testing.T) {
	fake := manyLunsDsm(5)
	service := newTestService(t, fake)

	if got := assertSameAsListing(t, service, "missing-uuid", fake); got != nil {
		t.Fatalf("found %+v for a missing volume", got)
	}
	// The full listing ran: one LunGet for the direct lookup, one per mapped LUN.
	if n := fake.count("SYNO.Core.ISCSI.LUN.get"); n != 1+len(fake.luns) {
		t.Errorf("LunGet called %d times, want %d", n, 1+len(fake.luns))
	}
}

func TestGetVolumeIgnoresLUNsTheDriverDidNotCreate(t *testing.T) {
	fake := manyLunsDsm(2)
	fake.luns[1].Name = "manually-created-lun"
	service := newTestService(t, fake)

	if got := assertSameAsListing(t, service, "lun-uuid-01", fake); got != nil {
		t.Fatalf("returned a LUN without the driver prefix: %+v", got)
	}
}

func TestGetVolumeIgnoresUnmappedLUN(t *testing.T) {
	fake := manyLunsDsm(2)
	fake.targets = fake.targets[:1]
	service := newTestService(t, fake)

	if got := assertSameAsListing(t, service, "lun-uuid-01", fake); got != nil {
		t.Fatalf("returned a LUN no target maps: %+v", got)
	}
}

func TestGetVolumeDoesNotMatchALUNByName(t *testing.T) {
	fake := manyLunsDsm(2)
	service := newTestService(t, fake)

	// LunGet also resolves names, but a volume ID is a UUID: the listing never
	// matches a LUN whose name equals the requested ID.
	if got := assertSameAsListing(t, service, fake.luns[1].Name, fake); got != nil {
		t.Fatalf("matched LUN %s by name", got.Name)
	}
}

func TestGetVolumeSkipsSharesOnUCDsm(t *testing.T) {
	fake := &fakeDsm{uc: true, shares: []webapi.ShareInfo{{
		Name:    models.GenShareName("pvc-share"),
		VolPath: "/volume1",
		Uuid:    "share-uuid-uc",
	}}}
	service := newTestService(t, fake)

	if got := assertSameAsListing(t, service, "share-uuid-uc", fake); got != nil {
		t.Fatalf("returned a share from a UC DSM: %+v", got)
	}
	if n := fake.count("SYNO.Core.Share.list"); n != 0 {
		t.Errorf("ShareList called %d times on a UC DSM, want 0", n)
	}
}

func TestGetVolumeSkipsShareWhosePrivilegeCannotBeLoaded(t *testing.T) {
	fake := &fakeDsm{shares: []webapi.ShareInfo{{
		Name:    models.GenShareName("pvc-share"),
		VolPath: "/volume1",
		Uuid:    "share-uuid-broken",
	}}}
	fake.brokenPrivileges = map[string]bool{fake.shares[0].Name: true}
	service := newTestService(t, fake)

	// The listing cannot tell NFS from SMB and skips the share; guessing would
	// report an NFS share as SMB.
	if got := assertSameAsListing(t, service, "share-uuid-broken", fake); got != nil {
		t.Fatalf("returned a share whose protocol is unknown: %+v", got)
	}
}
