// +build linux

package btrfs

/*
#include <stdlib.h>
#include <dirent.h>
#include <btrfs/ioctl.h>
*/
import "C"

import (
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"
	"unsafe"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/pkg/mount"
	"github.com/docker/docker/pkg/parsers"
	"github.com/docker/docker/pkg/units"
)

var (
	quotaSize    uint64
	useQuota     bool = false
	quotaEnabled bool = false
)

func init() {
	graphdriver.Register("btrfs", Init)
}

func Init(home string, options []string) (graphdriver.Driver, error) {
	rootdir := path.Dir(home)

	for _, option := range options {
		key, val, err := parsers.ParseKeyValueOpt(option)
		if err != nil {
			return nil, err
		}
		key = strings.ToLower(key)
		switch key {
		case "btrfs.quotasize":
			size, err := units.RAMInBytes(val)
			if err != nil {
				return nil, err
			}
			quotaSize = uint64(size)
			useQuota = true
			log.Infof("btrfs quota enabled: %d", quotaSize)
		default:
			return nil, fmt.Errorf("Unknown option %s\n", key)
		}
	}

	var buf syscall.Statfs_t
	if err := syscall.Statfs(rootdir, &buf); err != nil {
		return nil, err
	}

	if graphdriver.FsMagic(buf.Type) != graphdriver.FsMagicBtrfs {
		return nil, graphdriver.ErrPrerequisites
	}

	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}

	if err := mount.MakePrivate(home); err != nil {
		return nil, err
	}

	if useQuota && !quotaEnabled {
		if err := quotaEnable(rootdir); err != nil {
			return nil, err
		}
		quotaEnabled = true
		if err := quotaRescan(rootdir); err != nil {
			return nil, err
		}
	} else {
		if err := quotaDisable(rootdir); err != nil {
			return nil, err
		}
	}

	driver := &Driver{
		home: home,
	}

	return graphdriver.NaiveDiffDriver(driver), nil
}

type Driver struct {
	home string
}

func (d *Driver) String() string {
	return "btrfs"
}

func (d *Driver) Status() [][2]string {
	status := [][2]string{}
	if bv := BtrfsBuildVersion(); bv != "-" {
		status = append(status, [2]string{"Build Version", bv})
	}
	if lv := BtrfsLibVersion(); lv != -1 {
		status = append(status, [2]string{"Library Version", fmt.Sprintf("%d", lv)})
	}
	return status
}

func (d *Driver) Cleanup() error {
	return mount.Unmount(d.home)
}

func free(p *C.char) {
	C.free(unsafe.Pointer(p))
}

func openDir(path string) (*C.DIR, error) {
	Cpath := C.CString(path)
	defer free(Cpath)

	dir := C.opendir(Cpath)
	if dir == nil {
		return nil, fmt.Errorf("Can't open dir")
	}
	return dir, nil
}

func closeDir(dir *C.DIR) {
	if dir != nil {
		C.closedir(dir)
	}
}

func getDirFd(dir *C.DIR) uintptr {
	return uintptr(C.dirfd(dir))
}

func subvolCreate(path, name string) error {
	dir, err := openDir(path)
	if err != nil {
		return err
	}
	defer closeDir(dir)

	var args C.struct_btrfs_ioctl_vol_args
	for i, c := range []byte(name) {
		args.name[i] = C.char(c)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(dir), C.BTRFS_IOC_SUBVOL_CREATE,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to create btrfs subvolume: %v", errno.Error())
	}
	return nil
}

func subvolSnapshot(src, dest, name string) error {
	srcDir, err := openDir(src)
	if err != nil {
		return err
	}
	defer closeDir(srcDir)

	destDir, err := openDir(dest)
	if err != nil {
		return err
	}
	defer closeDir(destDir)

	var args C.struct_btrfs_ioctl_vol_args_v2
	args.fd = C.__s64(getDirFd(srcDir))
	for i, c := range []byte(name) {
		args.name[i] = C.char(c)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(destDir), C.BTRFS_IOC_SNAP_CREATE_V2,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to create btrfs snapshot: %v", errno.Error())
	}
	return nil
}

func quotaEnable(path string) error {
	log.Infof("enable btrfs quota on: %s", path)

	dir, err := openDir(path)
	if err != nil {
		return err
	}
	defer closeDir(dir)

	var args C.struct_btrfs_ioctl_quota_ctl_args
	args.cmd = C.BTRFS_QUOTA_CTL_ENABLE

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(dir), C.BTRFS_IOC_QUOTA_CTL,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to enable btrfs quota: %v", errno.Error())
	}
	return nil
}

func quotaDisable(path string) error {
	log.Infof("disable btrfs quota on: %s", path)

	dir, err := openDir(path)
	if err != nil {
		return err
	}
	defer closeDir(dir)

	var args C.struct_btrfs_ioctl_quota_ctl_args
	args.cmd = C.BTRFS_QUOTA_CTL_DISABLE

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(dir), C.BTRFS_IOC_QUOTA_CTL,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to disable btrfs quota: %v", errno.Error())
	}
	return nil
}

func subvolQuotaGroupLimit(path string) error {
	log.Infof("enable btrfs quota group limits on: %s", path)
	dir, err := openDir(path)
	if err != nil {
		return err
	}
	defer closeDir(dir)

	var args C.struct_btrfs_ioctl_qgroup_limit_args
	args.lim.max_referenced = C.__u64(quotaSize)

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(dir), C.BTRFS_IOC_QGROUP_LIMIT,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to set btrfs quota limits: %v", errno.Error())
	}
	return nil
}

func quotaRescan(path string) error {
	log.Infof("rescan for btrfs quota on: %s", path)

	dir, err := openDir(path)
	if err != nil {
		return err
	}
	defer closeDir(dir)

	var args C.struct_btrfs_ioctl_quota_rescan_args

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(dir), C.BTRFS_IOC_QUOTA_RESCAN,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to rescan btrfs quota: %v", errno.Error())
	}
	return nil
}

func subvolDelete(path, name string) error {
	dir, err := openDir(path)
	if err != nil {
		return err
	}
	defer closeDir(dir)

	var args C.struct_btrfs_ioctl_vol_args
	for i, c := range []byte(name) {
		args.name[i] = C.char(c)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, getDirFd(dir), C.BTRFS_IOC_SNAP_DESTROY,
		uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return fmt.Errorf("Failed to destroy btrfs snapshot: %v", errno.Error())
	}
	return nil
}

func (d *Driver) subvolumesDir() string {
	return path.Join(d.home, "subvolumes")
}

func (d *Driver) subvolumesDirId(id string) string {
	return path.Join(d.subvolumesDir(), id)
}

func (d *Driver) Create(id string, parent string) error {
	subvolumes := path.Join(d.home, "subvolumes")
	if err := os.MkdirAll(subvolumes, 0700); err != nil {
		return err
	}
	if parent == "" {
		if err := subvolCreate(subvolumes, id); err != nil {
			return err
		}
		if quotaEnabled {
			if err := subvolQuotaGroupLimit(subvolumes + "/" + id); err != nil {
				return err
			}
		}
	} else {
		parentDir, err := d.Get(parent, "")
		if err != nil {
			return err
		}
		if err := subvolSnapshot(parentDir, subvolumes, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) Remove(id string) error {
	dir := d.subvolumesDirId(id)
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	if err := subvolDelete(d.subvolumesDir(), id); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (d *Driver) Get(id, mountLabel string) (string, error) {
	dir := d.subvolumesDirId(id)
	st, err := os.Stat(dir)
	if err != nil {
		return "", err
	}

	if !st.IsDir() {
		return "", fmt.Errorf("%s: not a directory", dir)
	}

	return dir, nil
}

func (d *Driver) Put(id string) {
	// Get() creates no runtime resources (like e.g. mounts)
	// so this doesn't need to do anything.
}

func (d *Driver) Exists(id string) bool {
	dir := d.subvolumesDirId(id)
	_, err := os.Stat(dir)
	return err == nil
}
