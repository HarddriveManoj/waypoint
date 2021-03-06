// Package ceb contains the core logic for the custom entrypoint binary ("ceb").
package ceb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hashicorp/waypoint/internal/server"
	pb "github.com/hashicorp/waypoint/internal/server/gen"
	"github.com/hashicorp/waypoint/internal/version"
)

const (
	envDeploymentId        = "WAYPOINT_DEPLOYMENT_ID"
	envServerAddr          = "WAYPOINT_SERVER_ADDR"
	envServerTls           = "WAYPOINT_SERVER_TLS"
	envServerTlsSkipVerify = "WAYPOINT_SERVER_TLS_SKIP_VERIFY"
	envCEBDisable          = "WAYPOINT_CEB_DISABLE"
	envCEBServerRequired   = "WAYPOINT_CEB_SERVER_REQUIRED"
	envCEBToken            = "WAYPOINT_CEB_INVITE_TOKEN"
)

const (
	DefaultPort = 5000
)

// CEB represents the state of a running CEB.
type CEB struct {
	id           string
	deploymentId string
	logger       hclog.Logger
	context      context.Context
	client       pb.WaypointClient
	childCmd     *exec.Cmd
	execIdx      int64

	cleanupFunc func()

	urlAgentMu     sync.Mutex
	urlAgentCtx    context.Context
	urlAgentCancel func()
}

// Run runs a CEB with the given options.
//
// This will run until the context is cancelled. If the context is cancelled,
// we will attempt to gracefully exit the underlying program and attempt to
// clean up all resources.
func Run(ctx context.Context, os ...Option) error {
	// Create our ID
	id, err := server.Id()
	if err != nil {
		return status.Errorf(codes.Internal,
			"failed to generate unique ID: %s", err)
	}

	// Defaults, initialization
	ceb := &CEB{
		id:      id,
		logger:  hclog.L(),
		context: ctx,
	}
	defer ceb.Close()

	// Set our options
	var cfg config
	for _, o := range os {
		err := o(ceb, &cfg)
		if err != nil {
			return err
		}
	}

	ceb.logger.Info("entrypoint starting",
		"deployment_id", ceb.deploymentId,
		"instance_id", ceb.id,
		"args", cfg.ExecArgs,
	)

	vsn := version.GetVersion()
	ceb.logger.Info("entrypoint version",
		"full_string", vsn.FullVersionNumber(true),
		"version", vsn.Version,
		"prerelease", vsn.VersionPrerelease,
		"metadata", vsn.VersionMetadata,
		"revision", vsn.Revision,
	)

	// Initialize our command
	if err := ceb.initChildCmd(ctx, &cfg); err != nil {
		return status.Errorf(codes.Aborted,
			"failed to connect to server: %s", err)
	}

	// If we are enabled, initialize the CEB feature set.
	if !cfg.disable {
		if err := ceb.init(ctx, &cfg, false); err != nil {
			return err
		}
	}

	// Run our subprocess
	errCh := ceb.execChildCmd(ctx)
	select {
	case err := <-errCh:
		return err

	case <-ctx.Done():
		ceb.logger.Info("received cancellation request, gracefully exiting")
		ceb.childCmd.Process.Kill()
		<-errCh
	}

	return nil
}

// Close cleans up any resources created by the CEB and should be called
// to gracefully exit.
func (ceb *CEB) Close() error {
	if f := ceb.cleanupFunc; f != nil {
		f()
	}

	return nil
}

// cleanup stacks cleanup functions to call when Close is called.
func (ceb *CEB) cleanup(f func()) {
	oldF := ceb.cleanupFunc
	ceb.cleanupFunc = func() {
		defer f()
		if oldF != nil {
			oldF()
		}
	}
}

// DeploymentId returns the deployment ID that this CEB represents.
func (ceb *CEB) DeploymentId() string {
	return ceb.deploymentId
}

type config struct {
	disable             bool
	cebPtr              *CEB
	ExecArgs            []string
	ServerAddr          string
	ServerRequired      bool
	ServerTls           bool
	ServerTlsSkipVerify bool
	InviteToken         string

	URLServicePort int
}

type Option func(*CEB, *config) error

// WithEnvDefaults sets the configuration based on well-known accepted
// environment variables. If this is NOT called, then the environment variable
// based confiugration will be ignored.
func WithEnvDefaults() Option {
	return func(ceb *CEB, cfg *config) error {
		var port int
		portStr := os.Getenv("PORT")
		if portStr == "" {
			port = DefaultPort
			os.Setenv("PORT", strconv.Itoa(DefaultPort))
		} else {
			i, err := strconv.Atoi(portStr)
			if err != nil {
				return fmt.Errorf("Invalid value of PORT: %s", err)
			}

			port = i
		}

		cfg.URLServicePort = port
		cfg.ServerAddr = os.Getenv(envServerAddr)
		cfg.ServerRequired = os.Getenv(envCEBServerRequired) != ""
		cfg.ServerTls = os.Getenv(envServerTls) != ""
		cfg.ServerTlsSkipVerify = os.Getenv(envServerTlsSkipVerify) != ""
		cfg.InviteToken = os.Getenv(envCEBToken)
		cfg.disable = os.Getenv(envCEBDisable) != ""

		ceb.deploymentId = os.Getenv(envDeploymentId)

		return nil
	}
}

// WithExec sets the binary and arguments for the child process that the
// ceb execs. If the first value is not absolute then we'll look for it on
// the PATH.
func WithExec(args []string) Option {
	return func(ceb *CEB, cfg *config) error {
		cfg.ExecArgs = args
		return nil
	}
}

// WithClient specifies the Waypoint client to use directly. This will
// override any env vars or any other form of client connection configuration.
func WithClient(client pb.WaypointClient) Option {
	return func(ceb *CEB, cfg *config) error {
		ceb.client = client
		return nil
	}
}

// withCEBValue is used by tests to get the CEB struct pointer from Run.
// This is a nasty pattern but its encapsulated behind test helpers.
func withCEBValue(cebCh chan<- *CEB) Option {
	return func(ceb *CEB, cfg *config) error {
		cebCh <- ceb
		return nil
	}
}
