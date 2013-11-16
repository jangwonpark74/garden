package linux_container_pool

import (
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/vito/garden/backend"
	"github.com/vito/garden/backend/linux_backend"
	"github.com/vito/garden/backend/linux_backend/cgroups_manager"
	"github.com/vito/garden/backend/linux_backend/network"
	"github.com/vito/garden/command_runner"
)

type LinuxContainerPool struct {
	rootPath   string
	depotPath  string
	rootFSPath string

	uidPool     UIDPool
	networkPool NetworkPool

	runner command_runner.CommandRunner

	nextContainer int64

	sync.RWMutex
}

type UIDPool interface {
	Acquire() (uint32, error)
	Release(uint32)
}

type NetworkPool interface {
	Acquire() (network.Network, error)
	Release(network.Network)
}

func New(rootPath, depotPath, rootFSPath string, uidPool UIDPool, networkPool NetworkPool, runner command_runner.CommandRunner) *LinuxContainerPool {
	return &LinuxContainerPool{
		rootPath:   rootPath,
		depotPath:  depotPath,
		rootFSPath: rootFSPath,

		uidPool:     uidPool,
		networkPool: networkPool,

		runner: runner,

		nextContainer: time.Now().UnixNano(),
	}
}

func (p *LinuxContainerPool) Setup() error {
	setup := exec.Command(path.Join(p.rootPath, "setup.sh"))

	setup.Env = []string{
		"POOL_NETWORK=10.254.0.0/24",
		"ALLOW_NETWORKS=",
		"DENY_NETWORKS=",
		"CONTAINER_ROOTFS_PATH=" + p.rootFSPath,
		"CONTAINER_DEPOT_PATH=" + p.depotPath,
		"CONTAINER_DEPOT_MOUNT_POINT_PATH=/",
		"DISK_QUOTA_ENABLED=true",

		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	err := p.runner.Run(setup)
	if err != nil {
		return err
	}

	return nil
}

func (p *LinuxContainerPool) Create(spec backend.ContainerSpec) (backend.Container, error) {
	uid, err := p.uidPool.Acquire()
	if err != nil {
		return nil, err
	}

	network, err := p.networkPool.Acquire()
	if err != nil {
		p.uidPool.Release(uid)
		return nil, err
	}

	p.Lock()

	id := p.generateContainerID()

	p.Unlock()

	containerPath := path.Join(p.depotPath, id)

	cgroupsManager := cgroups_manager.New("/tmp/warden/cgroup", id)

	container := linux_backend.NewLinuxContainer(id, containerPath, spec, p.runner, cgroupsManager)

	create := exec.Command(
		path.Join(p.rootPath, "create.sh"),
		containerPath,
	)

	create.Env = []string{
		"id=" + container.ID(),
		"rootfs_path=" + p.rootFSPath,
		fmt.Sprintf("user_uid=%d", uid),
		fmt.Sprintf("network_host_ip=%s", network.HostIP()),
		fmt.Sprintf("network_container_ip=%s", network.ContainerIP()),
		"network_netmask=255.255.255.252",

		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	err = p.runner.Run(create)
	if err != nil {
		p.uidPool.Release(uid)
		p.networkPool.Release(network)
		return nil, err
	}

	return container, nil
}

func (p *LinuxContainerPool) Destroy(container backend.Container) error {
	destroy := exec.Command(
		path.Join(p.rootPath, "destroy.sh"),
		path.Join(p.depotPath, container.ID()),
	)

	destroy.Env = []string{
		"id=" + container.ID(),
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	err := p.runner.Run(destroy)
	if err != nil {
		return err
	}

	return nil
}

func (p *LinuxContainerPool) generateContainerID() string {
	p.nextContainer++

	containerID := []byte{}

	var i uint
	for i = 0; i < 11; i++ {
		containerID = strconv.AppendInt(
			containerID,
			(p.nextContainer>>(55-(i+1)*5))&31,
			32,
		)
	}

	return string(containerID)
}
