package commands

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/opentable/sous/core"
	"github.com/opentable/sous/deploy"
	"github.com/opentable/sous/tools"
	"github.com/opentable/sous/tools/cli"
	"github.com/opentable/sous/tools/cmd"
	"github.com/opentable/sous/tools/docker"
	"github.com/opentable/sous/tools/version"
	"github.com/opentable/sous/tools/yaml"
)

var (
	contractsFlags = flag.NewFlagSet("contracts", flag.ExitOnError)
	timeoutFlag    = contractsFlags.Duration("timeout", 10*time.Second, "per-contract timeout")
	dockerImage    = contractsFlags.String("image", "", "run contracts against a pre-built Docker image")
	contractName   = contractsFlags.String("contract", "", "run a single, named contract")
	checkNumber    = contractsFlags.Int("check", 0, "run a single check within the named contract (only available in conjunction with -contract)")
	listContracts  = contractsFlags.Bool("list", false, "list all contracts")
)

func ContractsHelp() string {
	return `sous contracts tests your project conforms to necessary contracts to run successfully on the OpenTable Mesos platform.`
}

func Contracts(sous *core.Sous, args []string) {
	contractsFlags.Parse(args)
	args = contractsFlags.Args()
	docker.RequireVersion(version.Range("^1.8.3"))
	docker.RequireDaemon()
	image := ""
	if dockerImage != nil {
		image = *dockerImage
	}
	if len(args) != 0 {
		cli.Fatalf("usage: sous contracts [-image <docker-image>]")
	}
	// If a docker image is not passed in, fall back to normal
	// sous project context to generate an image if necessary.
	if image == "" {
		t, c := sous.AssembleTargetContext("app")
		if yes, reason := sous.NeedsToBuildNewImage(t, c, false); yes {
			cli.Logf("Building new image because %s", reason)
			sous.RunTarget(t, c)
		}
		image = c.DockerTag()
	}

	contract := *contractName
	check := *checkNumber
	if check != 0 && contract == "" {
		cli.Fatalf("you specified -check but not -contract")
	}

	if !docker.ImageExists(image) {
		cli.Logf("Image %q not found locally; pulling...", image)
		docker.Pull(image)
	}

	getInitialValues := func() map[string]string { return map[string]string{"Image": image} }

	cc := NewConfiguredContracts(sous.State, getInitialValues)

	var err error
	if check != 0 {
		err = cc.RunSingleCheck(contract, check)
	} else if contract != "" {
		err = cc.RunSingleContract(contract)
	} else {
		err = cc.RunContractsForKind("http-service")
	}

	if err != nil {
		cli.Fatalf("%s", err)
	}

	cli.Success()
}

type ConfiguredContracts struct {
	Contracts     deploy.Contracts
	ContractDefs  map[string][]string
	InitialValues func() map[string]string
}

func NewConfiguredContracts(state *deploy.State, initialValues func() map[string]string) ConfiguredContracts {
	if err := state.Contracts.Validate(); err != nil {
		cli.Fatalf("Unable to run: %s", err)
	}
	return ConfiguredContracts{state.Contracts, state.ContractDefs, initialValues}
}

func (cc ConfiguredContracts) RunContractsForKind(kind string) error {
	for _, name := range cc.ContractDefs[kind] {
		if err := cc.RunSingleContract(name); err != nil {
			return fmt.Errorf("running contracts for %q; %s", kind, err)
		}
	}
	return nil
}

func (cc ConfiguredContracts) RunSingleContract(name string) error {
	initialValues := cc.InitialValues()
	contract, ok := cc.Contracts[name]
	if !ok {
		return fmt.Errorf("contract %q not found.", name)
	}
	run := NewContractRun(contract, initialValues)
	if err := run.Execute(); err != nil {
		return fmt.Errorf("contract %q failed: %s", contract.Name, err)
	}
	return nil
}

func (cc ConfiguredContracts) RunSingleCheck(name string, check int) error {
	initialValues := cc.InitialValues()
	contract, ok := cc.Contracts[name]
	if !ok {
		return fmt.Errorf("contract %q not found.", name)
	}
	run := NewContractRun(contract, initialValues)
	if err := run.ExecuteUpToCheck(check); err != nil {
		return fmt.Errorf("contract %q failed: %s", contract.Name, err)
	}
	return nil
}

// ContractRun is a single execution of a contract. It is also the struct passed
// in when resolving templated values in the contract definition YAML.
type ContractRun struct {
	Contract deploy.Contract
	// GlobalValues is shared between all servers started by the contract.
	// Once written, no item in GlobalValues should ever be changed.
	GlobalValues map[string]string
	// Values contains the resolved values for a specific contract. Typically
	// these are defined as Go text templated values in the contract definition
	// YAML.
	Values map[string]string
	// Servers contains all the started servers for this contract run.
	Servers       map[string]*StartedServer
	Preconditions []string
	Checks        []string
}

func NewContractRun(contract deploy.Contract, initialValues map[string]string) *ContractRun {
	if initialValues == nil {
		initialValues = map[string]string{}
	}
	return &ContractRun{
		Contract:     contract,
		GlobalValues: initialValues,
		Servers:      map[string]*StartedServer{},
	}
}

// ExecuteUpToCheck executes the first n checks. It is mainly used for
// testing.
func (r *ContractRun) ExecuteUpToCheck(n int) error {
	c := r.Contract
	cli.Logf("** ==> Running contract: %q**", c.Name)

	// First make sure all the necessary servers are started, in the correct order.
	for _, serverName := range c.StartServers {
		if err := r.StartServer(serverName); err != nil {
			return err
		}
	}

	// Second, resolve templated Values map in the contract, in light of the
	// started servers and everything else inside the ContractRun at this point.
	var values map[string]string
	if err := yaml.InjectTemplatePipeline(c.Values, &values, r); err != nil {
		return err
	}
	r.Values = values

	// Third, resolve all the other templated values in the contract using the special
	// Values map. (This is done in 2 stages to make the contracts significantly more
	// readable.
	if err := yaml.InjectTemplatePipeline(c, &c, values); err != nil {
		return err
	}

	// Next execute all the precondition checks to ensure we can meaningfully
	// run the main contract checks.
	for _, p := range c.Preconditions {
		if err := ExecuteCheck(p); err != nil {
			return fmt.Errorf("Precondition %q failed: %s", p.String(), err)
		}
		cli.Verbosef(" ==> Precondition **%s** passed.", p)
	}

	// Finally run the actual contract checks.
	for i := 0; i < n; i++ {
		check := c.Checks[i]
		if err := ExecuteCheck(check); err != nil {
			return fmt.Errorf("     check failed: %s; %s", check, err)
		}
		cli.Logf("     check passed: **%s**", check)
	}

	return nil
}

// Execute executes the entire contract.
func (r *ContractRun) Execute() error {
	return r.ExecuteUpToCheck(len(r.Contract.Checks))
}

func ExecuteCheck(c deploy.Check, progressTitle ...string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	return c.Execute()
}

func (r *ContractRun) StartServer(serverName string) error {
	c := r.Contract
	s, ok := c.Servers[serverName]
	if !ok {
		return fmt.Errorf("Contract %q specifies %s in StartServers, but no server with that name exists", c.Name, serverName)
	}
	resolvedServer, err := r.ResolveServer(s)
	if err != nil {
		return err
	}
	server, err := resolvedServer.Start()
	if err != nil {
		return err
	}
	cli.Verbosef("Started server %q (%s) as %s", serverName, resolvedServer.Docker.Image, server.Container.CID())
	cli.AddCleanupTask(func() error {
		if !server.Container.Running() {
			cli.Verbosef("Not stopping %q container (%s), it had already stopped.", server.ResolvedServer.Name, server.ContainerID)
			return nil
		}
		if err := server.Container.KillIfRunning(); err != nil {
			cli.Logf("Failed to stop %q container (%s)", serverName, server.Container.CID())
		} else {
			cli.Verbosef("Stopped %q container (%s)", serverName, server.Container.CID())
		}
		return err
	})
	r.Servers[serverName] = server
	return nil
}

// ResolvedServer is a *deploy.TestServer whose templated values
// have all been expanded, and is thus ready to be run.
type ResolvedServer deploy.TestServer

type StartedServer struct {
	*ResolvedServer
	// ContainerID is used in contract definitions to address the container.
	ContainerID string
	Container   docker.Container
}

// ResolveServer fleshes out all templated values in the server in the
// context of the current contract run, adding values to the .GlobalValues
// map if they aren't yet set.
func (r *ContractRun) ResolveServer(s deploy.TestServer) (*ResolvedServer, error) {
	cli.Verbosef("Resolving values for server %q", s.Name)
	for k, v := range s.DefaultValues {
		// Don't use default value if we already have that value in the global agglomeration.
		if v, ok := r.GlobalValues[k]; ok {
			cli.Verbosef(" ==> %s=%q (already set)", k, v)
			continue
		}
		v = tools.TrimWhitespace(v)
		if !strings.HasPrefix(v, "$(") {
			r.GlobalValues[k] = v
			cli.Verbosef(" ==> %s=%q", k, v)
			continue
		}
		v = trimPrefixAndSuffix(v, "$(", ")")
		result := cmd.Stdout("/bin/sh", "-c", v)
		r.GlobalValues[k] = result
		cli.Verbosef(" ==> %s=%q (%s)", k, result, v)
	}

	var ss deploy.TestServer
	if err := yaml.InjectTemplatePipeline(s, &ss, r.GlobalValues); err != nil {
		return nil, err
	}

	rs := ResolvedServer(ss)
	return &rs, nil
}

func (s *ResolvedServer) Start() (*StartedServer, error) {
	if !docker.ImageExists(s.Docker.Image) {
		cli.Logf("Image %q does not exist, beginning pull...", s.Docker.Image)
		docker.Pull(s.Docker.Image)
		if !docker.ImageExists(s.Docker.Image) {
			return nil, fmt.Errorf("Docker image %q still missing after pull", s.Docker.Image)
		}
	}
	if !docker.ExactlyOneImageExists(s.Docker.Image) {
		return nil, fmt.Errorf("Docker tag %q points to multiple images", s.Docker.Image)
	}
	run, err := s.MakeDockerRun()
	if err != nil {
		return nil, err
	}
	run.StdoutFile = "/dev/null"
	run.StderrFile = "/dev/null"
	cli.Verbosef("shell> %s", run.CalculatedCommand())
	container, err := run.Start()
	if err != nil {
		return nil, err
	}
	startedServer := &StartedServer{s, container.CID(), container}
	if s.Startup != nil {
		if err := ExecuteCheck(*s.Startup.CompleteWhen, fmt.Sprintf("Waiting for %s server", s.Name)); err != nil {
			return nil, fmt.Errorf("%s failed to start within the timeout (%s): %s", s.Docker.Image, s.Startup.CompleteWhen.Timeout, err)
		}
	}
	return startedServer, nil
}

func (s *ResolvedServer) MakeDockerRun() (*docker.Run, error) {
	d := s.Docker
	run := docker.NewRun(d.Image)
	for k, v := range d.Env {
		run.AddEnv(k, v)
	}
	run.Args = d.Args
	return run, nil
}

func trimPrefixAndSuffix(s, prefix, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(s, prefix), suffix)
}
