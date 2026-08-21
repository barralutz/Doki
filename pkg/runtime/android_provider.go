package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/OpceanAI/Doki/pkg/common"
)

// WorkloadMount preserves the logical Docker mount and carries an optional
// execution-time host path for Android-native providers.
type WorkloadMount struct {
	Mount    common.Mount
	HostPath string
}

// WorkloadDescriptor is the immutable-by-convention container contract a
// provider evaluates and prepares.
type WorkloadDescriptor struct {
	ImageRef    string
	ImageDigest string
	Platform    string
	ImageConfig *ImageOCIConfig
	Args        []string
	Env         []string
	Cwd         string
	User        string
	Labels      map[string]string
	Mounts      []WorkloadMount
	Ports       []common.Port
	HealthCheck *HealthCheckConfig
	StopSignal  string
}

// ProviderMatch describes whether a provider recognizes the workload and
// whether Android-native adaptation is required.
type ProviderMatch struct {
	Matched  bool
	Required bool
	Reason   string
}

// PreparedWorkload is a host process specification returned by a provider.
type PreparedWorkload struct {
	Executable string
	Args       []string
	Env        []string
	Cwd        string
}

// PreparedExec is a host exec process specification returned by a provider.
type PreparedExec struct {
	Executable string
	Args       []string
	Env        []string
	Cwd        string
}

// AndroidWorkloadProvider translates an OCI workload into a registered
// Android-native implementation.
type AndroidWorkloadProvider interface {
	ID() string
	Match(context.Context, WorkloadDescriptor) ProviderMatch
	Ensure(context.Context, WorkloadDescriptor) error
	Prepare(context.Context, WorkloadDescriptor) (*PreparedWorkload, error)
	PrepareExec(context.Context, WorkloadDescriptor, *ExecConfig) (*PreparedExec, error)
	Cleanup(context.Context, WorkloadDescriptor) error
}

// ProviderSelection is a deterministic required provider match.
type ProviderSelection struct {
	Provider AndroidWorkloadProvider
	Match    ProviderMatch
}

// AndroidProviderRegistry stores allowlisted Android workload providers.
type AndroidProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]AndroidWorkloadProvider
}

func NewAndroidProviderRegistry() *AndroidProviderRegistry {
	return &AndroidProviderRegistry{providers: make(map[string]AndroidWorkloadProvider)}
}

func (r *AndroidProviderRegistry) Register(p AndroidWorkloadProvider) error {
	if p == nil {
		return fmt.Errorf("Android provider is nil")
	}
	id := strings.TrimSpace(p.ID())
	if id == "" {
		return fmt.Errorf("Android provider ID is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[id]; exists {
		return fmt.Errorf("Android provider %q already registered", id)
	}
	r.providers[id] = p
	return nil
}

func (r *AndroidProviderRegistry) Get(id string) AndroidWorkloadProvider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[id]
}

func (r *AndroidProviderRegistry) SelectRequired(ctx context.Context, desc WorkloadDescriptor) (*ProviderSelection, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.RLock()
	ids := make([]string, 0, len(r.providers))
	providers := make(map[string]AndroidWorkloadProvider, len(r.providers))
	for id, p := range r.providers {
		ids = append(ids, id)
		providers[id] = p
	}
	r.mu.RUnlock()
	sort.Strings(ids)

	var required []ProviderSelection
	for _, id := range ids {
		p := providers[id]
		match := p.Match(ctx, desc)
		if match.Matched && match.Required {
			required = append(required, ProviderSelection{Provider: p, Match: match})
		}
	}
	switch len(required) {
	case 0:
		return nil, nil
	case 1:
		return &required[0], nil
	default:
		matchedIDs := make([]string, 0, len(required))
		for _, sel := range required {
			matchedIDs = append(matchedIDs, sel.Provider.ID())
		}
		return nil, fmt.Errorf("multiple Android providers require workload: %s", strings.Join(matchedIDs, ", "))
	}
}

func descriptorFromConfig(cfg *Config, mounts []WorkloadMount) WorkloadDescriptor {
	if cfg == nil {
		return WorkloadDescriptor{}
	}
	desc := WorkloadDescriptor{
		ImageRef:    cfg.ImageRef,
		ImageDigest: cfg.ImageDigest,
		Platform:    cfg.Platform,
		ImageConfig: cloneImageOCIConfig(cfg.ImageConfig),
		Args:        append([]string(nil), cfg.Args...),
		Env:         append([]string(nil), cfg.Env...),
		Cwd:         cfg.Cwd,
		User:        cfg.User,
		Labels:      cloneStringMap(cfg.Labels),
		Mounts:      append([]WorkloadMount(nil), mounts...),
		Ports:       append([]common.Port(nil), cfg.Ports...),
		HealthCheck: cloneHealthCheckConfig(cfg.HealthCheck),
		StopSignal:  cfg.StopSignal,
	}
	return desc
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneHealthCheckConfig(src *HealthCheckConfig) *HealthCheckConfig {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Test = append([]string(nil), src.Test...)
	return &dst
}

func cloneImageOCIConfig(src *ImageOCIConfig) *ImageOCIConfig {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Entrypoint = append([]string(nil), src.Entrypoint...)
	dst.Cmd = append([]string(nil), src.Cmd...)
	dst.Env = append([]string(nil), src.Env...)
	dst.Shell = append([]string(nil), src.Shell...)
	dst.Labels = cloneStringMap(src.Labels)
	dst.HealthCheck = cloneHealthCheckConfig(src.HealthCheck)
	if src.Volumes != nil {
		dst.Volumes = make(map[string]struct{}, len(src.Volumes))
		for k, v := range src.Volumes {
			dst.Volumes[k] = v
		}
	}
	return &dst
}

func (rt *Runtime) selectContainerExecution(ctx context.Context, cfg *Config) (ExecutionMode, *ProviderSelection, error) {
	if cfg != nil && strings.TrimSpace(cfg.Runtime) != "" {
		requested := strings.TrimSpace(cfg.Runtime)
		if requested != "android-native" {
			// Preserve legacy runtime behavior in Phase 2 while preventing an
			// automatic provider from overriding an explicit Docker runtime.
			return rt.mode, nil, nil
		}
		sel, err := rt.androidProviders.SelectRequired(ctx, descriptorFromConfig(cfg, nil))
		if err != nil {
			return 0, nil, err
		}
		if sel == nil {
			return 0, nil, fmt.Errorf("android-native runtime requested but no provider requires workload")
		}
		return ModeAndroidNative, sel, nil
	}

	if rt.androidProviders == nil {
		return rt.mode, nil, nil
	}
	sel, err := rt.androidProviders.SelectRequired(ctx, descriptorFromConfig(cfg, nil))
	if err != nil {
		return 0, nil, err
	}
	if sel == nil {
		return rt.mode, nil, nil
	}
	return ModeAndroidNative, sel, nil
}
