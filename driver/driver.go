package steamcmd

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
	pstructs "github.com/hashicorp/nomad/plugins/shared/structs"
)

const (
	pluginName    = "steamcmd"
	pluginVersion = "v0.1.0"

	fingerprintPeriod = 30 * time.Second
)

var PluginInfo = &base.PluginInfoResponse{
	Type:              base.PluginTypeDriver,
	PluginApiVersions: []string{drivers.ApiVersion010},
	PluginVersion:     pluginVersion,
	Name:              pluginName,
}

// Capabilities declares what this driver supports. Notably: no FSIsolation
// (we deliberately run raw_exec-style, no chroot/namespace) so that CSI
// volume mounts staged by Nomad are visible to steamcmd/the launched
// process without any bind-mount plumbing on our part, and NetIsolationModes
// is host-only for the same reason.
var driverCapabilities = &drivers.Capabilities{
	SendSignals: true,
	Exec:        false,
	FSIsolation: drivers.FSIsolationNone,
	NetIsolationModes: []drivers.NetIsolationMode{
		drivers.NetIsolationModeHost,
	},
	MustInitiateNetwork: false,
	MountConfigs:        drivers.MountConfigSupportAll,
}

// Driver implements drivers.DriverPlugin for steamcmd-managed tasks.
type Driver struct {
	eventer     *drivers.Eventer
	config      PluginConfig
	nomadConfig *base.ClientAgentConfig

	tasks  sync.Map // task ID -> *taskHandle
	ctx    context.Context
	cancel context.CancelFunc
	logger hclog.Logger

	installSem chan struct{} // bounds concurrent steamcmd installs if MaxConcurrent > 0
}

func NewPlugin(logger hclog.Logger) drivers.DriverPlugin {
	ctx, cancel := context.WithCancel(context.Background())
	logger = logger.Named(pluginName)
	return &Driver{
		eventer: drivers.NewEventer(ctx, logger),
		config:  PluginConfig{SteamCmdPath: "steamcmd"},
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger,
	}
}

func (d *Driver) PluginInfo() (*base.PluginInfoResponse, error) {
	return PluginInfo, nil
}

func (d *Driver) ConfigSchema() (*hclspec.Spec, error) {
	return configSpec, nil
}

func (d *Driver) SetConfig(cfg *base.Config) error {
	var pluginConfig PluginConfig
	if len(cfg.PluginConfig) > 0 {
		if err := base.MsgPackDecode(cfg.PluginConfig, &pluginConfig); err != nil {
			return err
		}
	}
	if pluginConfig.SteamCmdPath == "" {
		pluginConfig.SteamCmdPath = "steamcmd"
	}
	d.config = pluginConfig
	if cfg.AgentConfig != nil {
		d.nomadConfig = cfg.AgentConfig.Driver
	}
	if pluginConfig.MaxConcurrent > 0 {
		d.installSem = make(chan struct{}, pluginConfig.MaxConcurrent)
	} else {
		d.installSem = nil
	}
	return nil
}

func (d *Driver) TaskConfigSchema() (*hclspec.Spec, error) {
	return taskConfigSpec, nil
}

func (d *Driver) Capabilities() (*drivers.Capabilities, error) {
	return driverCapabilities, nil
}

// Fingerprint reports node health/attributes at fingerprintPeriod. A node
// is only eligible to schedule steamcmd tasks if the binary is present and
// executable -- this is what lets job specs rely on the scheduler placing
// work only where steamcmd actually exists, rather than failing at
// StartTask time.
func (d *Driver) Fingerprint(ctx context.Context) (<-chan *drivers.Fingerprint, error) {
	ch := make(chan *drivers.Fingerprint)
	go d.handleFingerprint(ctx, ch)
	return ch, nil
}

func (d *Driver) handleFingerprint(ctx context.Context, ch chan<- *drivers.Fingerprint) {
	defer close(ch)
	ticker := time.NewTicker(fingerprintPeriod)
	defer ticker.Stop()

	for {
		ch <- d.buildFingerprint()
		select {
		case <-ctx.Done():
			return
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Driver) buildFingerprint() *drivers.Fingerprint {
	binPath, err := exec.LookPath(d.config.SteamCmdPath)
	if err != nil {
		return &drivers.Fingerprint{
			Health:            drivers.HealthStateUndetected,
			HealthDescription: fmt.Sprintf("steamcmd not found on PATH (%s): %v", d.config.SteamCmdPath, err),
		}
	}

	probeCtx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	out, verErr := exec.CommandContext(probeCtx, binPath, "+quit").CombinedOutput()
	attrs := map[string]*pstructs.Attribute{
		"driver.steamcmd":      pstructs.NewBoolAttribute(true),
		"driver.steamcmd.path": pstructs.NewStringAttribute(binPath),
	}
	if verErr != nil {
		d.logger.Warn("steamcmd version probe failed, marking unhealthy", "error", verErr, "output", string(out))
		return &drivers.Fingerprint{
			Attributes:        attrs,
			Health:            drivers.HealthStateUnhealthy,
			HealthDescription: fmt.Sprintf("steamcmd probe failed: %v", verErr),
		}
	}

	return &drivers.Fingerprint{
		Attributes:        attrs,
		Health:            drivers.HealthStateHealthy,
		HealthDescription: "steamcmd ready",
	}
}

func (d *Driver) acquireInstallSlot(ctx context.Context) func() {
	if d.installSem == nil {
		return func() {}
	}
	select {
	case d.installSem <- struct{}{}:
	case <-ctx.Done():
		return func() {}
	}
	return func() { <-d.installSem }
}
