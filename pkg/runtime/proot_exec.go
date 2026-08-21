package runtime

import (
	"github.com/OpceanAI/Doki/internal/proot"
	"github.com/OpceanAI/Doki/pkg/common"
)

func (rt *Runtime) buildProotExecArgs(rootfs string, mounts []common.Mount, workingDir, user string, command []string) ([]string, error) {
	uid, gid := parseUser(user)
	args, err := proot.BuildProotBaseArgs(rootfs, uid, gid)
	if err != nil {
		return nil, err
	}
	if rt.isAndroid() {
		args = proot.AppendAndroidBinds(args)
	}
	args, err = rt.appendProotMountArgs(args, rootfs, mounts)
	if err != nil {
		return nil, err
	}
	guestWD := workingDir
	if guestWD == "" {
		guestWD = "/"
	}
	args = append(args, "-w", guestWD)
	args = append(args, command...)
	return args, nil
}
