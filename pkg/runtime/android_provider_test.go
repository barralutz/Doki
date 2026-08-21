package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpceanAI/Doki/pkg/common"
)

type fakeAndroidProvider struct {
	id               string
	match            ProviderMatch
	ensureErr        error
	prepareErr       error
	prepareExecErr   error
	cleanupErr       error
	prepared         *PreparedWorkload
	preparedExec     *PreparedExec
	ensureCalls      int
	prepareCalls     int
	prepareExecCalls int
	cleanupCalls     int
	lastDescriptor   WorkloadDescriptor
	lastExec         *ExecConfig
}

func (f *fakeAndroidProvider) ID() string { return f.id }
func (f *fakeAndroidProvider) Match(context.Context, WorkloadDescriptor) ProviderMatch {
	return f.match
}
func (f *fakeAndroidProvider) Ensure(_ context.Context, desc WorkloadDescriptor) error {
	f.ensureCalls++
	f.lastDescriptor = desc
	return f.ensureErr
}
func (f *fakeAndroidProvider) Prepare(_ context.Context, desc WorkloadDescriptor) (*PreparedWorkload, error) {
	f.prepareCalls++
	f.lastDescriptor = desc
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	if f.prepared != nil {
		cp := *f.prepared
		cp.Args = append([]string(nil), f.prepared.Args...)
		cp.Env = append([]string(nil), f.prepared.Env...)
		return &cp, nil
	}
	return &PreparedWorkload{}, nil
}
func (f *fakeAndroidProvider) PrepareExec(_ context.Context, desc WorkloadDescriptor, cfg *ExecConfig) (*PreparedExec, error) {
	f.prepareExecCalls++
	f.lastDescriptor = desc
	if cfg != nil {
		cp := *cfg
		cp.Args = append([]string(nil), cfg.Args...)
		cp.Env = append([]string(nil), cfg.Env...)
		f.lastExec = &cp
	}
	if f.prepareExecErr != nil {
		return nil, f.prepareExecErr
	}
	if f.preparedExec != nil {
		cp := *f.preparedExec
		cp.Args = append([]string(nil), f.preparedExec.Args...)
		cp.Env = append([]string(nil), f.preparedExec.Env...)
		return &cp, nil
	}
	return &PreparedExec{}, nil
}
func (f *fakeAndroidProvider) Cleanup(_ context.Context, desc WorkloadDescriptor) error {
	f.cleanupCalls++
	f.lastDescriptor = desc
	return f.cleanupErr
}

func TestAndroidProviderRegistrySelectRequired(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	if err := reg.Register(&fakeAndroidProvider{id: "one", match: ProviderMatch{Matched: true, Required: true, Reason: "needs android"}}); err != nil {
		t.Fatal(err)
	}
	sel, err := reg.SelectRequired(context.Background(), WorkloadDescriptor{ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}
	if sel == nil || sel.Provider.ID() != "one" || sel.Match.Reason != "needs android" {
		t.Fatalf("selection = %+v", sel)
	}
}

func TestAndroidProviderRegistryNoRequiredMatch(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(&fakeAndroidProvider{id: "none", match: ProviderMatch{Matched: false}})
	_ = reg.Register(&fakeAndroidProvider{id: "recognized", match: ProviderMatch{Matched: true, Required: false, Reason: "works in proot"}})
	sel, err := reg.SelectRequired(context.Background(), WorkloadDescriptor{})
	if err != nil {
		t.Fatal(err)
	}
	if sel != nil {
		t.Fatalf("selection = %+v, want nil", sel)
	}
}

func TestAndroidProviderRegistryRejectsAmbiguousRequiredMatches(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(&fakeAndroidProvider{id: "zeta", match: ProviderMatch{Matched: true, Required: true}})
	_ = reg.Register(&fakeAndroidProvider{id: "alpha", match: ProviderMatch{Matched: true, Required: true}})
	_, err := reg.SelectRequired(context.Background(), WorkloadDescriptor{})
	if err == nil || !strings.Contains(err.Error(), "multiple Android providers require workload: alpha, zeta") {
		t.Fatalf("error = %v", err)
	}
}

func TestAndroidProviderRegistryRejectsInvalidIDs(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	if err := reg.Register(&fakeAndroidProvider{id: ""}); err == nil {
		t.Fatal("empty provider ID unexpectedly accepted")
	}
	if err := reg.Register(&fakeAndroidProvider{id: "dup"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&fakeAndroidProvider{id: "dup"}); err == nil {
		t.Fatal("duplicate provider ID unexpectedly accepted")
	}
}

func TestDescriptorFromConfigDoesNotAliasConfig(t *testing.T) {
	cfg := &Config{
		ImageRef:    "example:1",
		ImageDigest: "sha256:abc",
		Platform:    "linux/arm64",
		Args:        []string{"run", "one"},
		Env:         []string{"A=1"},
		Labels:      map[string]string{"k": "v"},
		Mounts:      []common.Mount{{Type: common.MountVolume, Source: "db", Target: "/data"}},
		Ports:       []common.Port{{PrivatePort: 5432, PublicPort: 5750, Type: common.ProtocolTCP}},
	}
	desc := descriptorFromConfig(cfg, []WorkloadMount{{Mount: cfg.Mounts[0], HostPath: "/host/db"}})
	desc.Args[0] = "changed"
	desc.Env[0] = "A=2"
	desc.Labels["k"] = "changed"
	desc.Mounts[0].Mount.Source = "changed"
	desc.Ports[0].PrivatePort = 1
	if cfg.Args[0] != "run" || cfg.Env[0] != "A=1" || cfg.Labels["k"] != "v" || cfg.Mounts[0].Source != "db" || cfg.Ports[0].PrivatePort != 5432 {
		t.Fatalf("descriptor aliases config: cfg=%+v", cfg)
	}
}

func TestRuntimeSelectContainerExecutionUsesRequiredProvider(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(&fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true, Reason: "test"}})
	rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
	mode, sel, err := rt.selectContainerExecution(context.Background(), &Config{ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeAndroidNative || sel == nil || sel.Provider.ID() != "fake" {
		t.Fatalf("mode=%v selection=%+v", mode, sel)
	}
}

func TestRuntimeSelectContainerExecutionFallsBackWithoutProvider(t *testing.T) {
	rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(NewAndroidProviderRegistry()))
	mode, sel, err := rt.selectContainerExecution(context.Background(), &Config{ImageRef: "example:1"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != rt.mode || sel != nil {
		t.Fatalf("mode=%v legacy=%v selection=%+v", mode, rt.mode, sel)
	}
}

func TestRuntimeSelectContainerExecutionExplicitRuntimeBehavior(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(&fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true}})
	rt := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(reg))
	mode, sel, err := rt.selectContainerExecution(context.Background(), &Config{Runtime: "proot"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != rt.mode || sel != nil {
		t.Fatalf("explicit legacy runtime unexpectedly adapted: mode=%v sel=%+v", mode, sel)
	}

	empty := NewRuntime(t.TempDir(), nil, WithAndroidProviderRegistry(NewAndroidProviderRegistry()))
	if _, _, err := empty.selectContainerExecution(context.Background(), &Config{Runtime: "android-native"}); err == nil || !strings.Contains(err.Error(), "android-native runtime requested but no provider requires workload") {
		t.Fatalf("explicit android-native without provider error=%v", err)
	}
}

func TestRuntimeCreateAndroidNativeSkipsInvalidOCIExtraction(t *testing.T) {
	reg := NewAndroidProviderRegistry()
	_ = reg.Register(&fakeAndroidProvider{id: "fake", match: ProviderMatch{Matched: true, Required: true, Reason: "test adapter"}})
	root := t.TempDir()
	badLayer := filepath.Join(root, "bad-layer.tar")
	if err := os.WriteFile(badLayer, []byte("not a tar archive"), 0644); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(filepath.Join(root, "runtime"), nil, WithAndroidProviderRegistry(reg))
	state, err := rt.Create(&Config{ID: "android-one", ImageRef: "example:1", ImageLayers: []string{badLayer}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != ModeAndroidNative {
		t.Fatalf("mode=%v", state.Mode)
	}
	if state.Config.RootfsReady != "" {
		t.Fatalf("RootfsReady=%q, want empty", state.Config.RootfsReady)
	}
	ps, err := rt.loadAndroidProviderState("android-one")
	if err != nil {
		t.Fatal(err)
	}
	if ps.ProviderID != "fake" || ps.MatchReason != "test adapter" {
		t.Fatalf("provider state=%+v", ps)
	}
}
