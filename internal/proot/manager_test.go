package proot

import "testing"

func TestBuildProotBaseArgsIncludesSysVIPC(t *testing.T) {
	rootfs := t.TempDir()
	args, err := BuildProotBaseArgs(rootfs, 70, 70)
	if err != nil {
		t.Fatalf("BuildProotBaseArgs() error = %v", err)
	}
	for _, arg := range args {
		if arg == "--sysvipc" {
			return
		}
	}
	t.Fatalf("BuildProotBaseArgs() = %#v, missing --sysvipc", args)
}
