/*
 * Copyright 2021 Synology Inc.
 */

package driver

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
)

func TestUsageFromStatfsReportsBytesAndInodes(t *testing.T) {
	statfs := &unix.Statfs_t{}
	statfs.Bsize = 4096
	statfs.Blocks = 1000
	statfs.Bfree = 400
	statfs.Bavail = 300
	statfs.Files = 50
	statfs.Ffree = 20

	usage := usageFromStatfs(statfs)
	if len(usage) != 2 {
		t.Fatalf("got %d usage entries, want bytes and inodes", len(usage))
	}

	bytes := usage[0]
	if bytes.Unit != csi.VolumeUsage_BYTES {
		t.Fatalf("first entry unit = %v, want BYTES", bytes.Unit)
	}
	if bytes.Total != 1000*4096 || bytes.Used != 600*4096 || bytes.Available != 300*4096 {
		t.Errorf("bytes = total %d used %d available %d", bytes.Total, bytes.Used, bytes.Available)
	}

	inodes := usage[1]
	if inodes.Unit != csi.VolumeUsage_INODES {
		t.Fatalf("second entry unit = %v, want INODES", inodes.Unit)
	}
	if inodes.Total != 50 || inodes.Used != 30 || inodes.Available != 20 {
		t.Errorf("inodes = total %d used %d available %d", inodes.Total, inodes.Used, inodes.Available)
	}
}

// An NFS share on btrfs counts no inodes. Reporting zeros would publish an
// inode metric that reads as an empty filesystem.
func TestUsageFromStatfsLeavesOutInodesWhenNoneAreCounted(t *testing.T) {
	statfs := &unix.Statfs_t{}
	statfs.Bsize = 1048576
	statfs.Blocks = 153600
	statfs.Bfree = 104448
	statfs.Bavail = 104448

	usage := usageFromStatfs(statfs)
	if len(usage) != 1 {
		t.Fatalf("got %d usage entries, want bytes only", len(usage))
	}
	if usage[0].Unit != csi.VolumeUsage_BYTES || usage[0].Used != (153600-104448)*1048576 {
		t.Errorf("bytes entry = %+v", usage[0])
	}
}
