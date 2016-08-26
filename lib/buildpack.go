package sous

import (
	"fmt"
	"strings"
	"time"
)

type (
	// A Selector selects the buildpack for a given build context
	Selector interface {
		SelectBuildpack(*BuildContext) (Buildpack, error)
	}

	// Labeller defines a container-based build system.
	Labeller interface {
		ApplyMetadata(*BuildResult, *BuildContext) error
	}

	// Registrar defines the interface to register build results to be deployed
	// later
	Registrar interface {
		// Register takes a BuildResult and makes it available for the deployment
		// target system to find during deployment
		Register(*BuildResult, *BuildContext) error
	}

	// BuildArtifact describes the actual built binary Sous will deploy
	BuildArtifact struct {
		Name, Type string
		Qualities  []Quality
	}

	// A Quality represents a characteristic of a BuildArtifact that needs to be recorded.
	Quality struct {
		Name string
		// Kind is the the kind of this quality
		// Known kinds include: advisory
		Kind string
	}

	// Buildpack is a set of instructions used to build a particular
	// kind of project.
	Buildpack interface {
		Detect(*BuildContext) (*DetectResult, error)
		Build(*BuildContext) (*BuildResult, error)
	}

	// DetectResult represents the result of a detection.
	DetectResult struct {
		Compatible  bool
		Description string
		Data        interface{}
	}
	// BuildResult represents the result of a build made with a Buildpack.
	BuildResult struct {
		ImageID                   string
		VersionName, RevisionName string
		Advisories                []string
		Elapsed                   time.Duration
	}

	EchoSelector struct {
		Factory func(*BuildContext) (Buildpack, error)
	}
)

func (s *EchoSelector) SelectBuildpack(c *BuildContext) (Buildpack, error) {
	return s.Factory(c)
}

func (br *BuildResult) String() string {
	str := fmt.Sprintf("Built: %q", br.VersionName)
	if len(br.Advisories) > 0 {
		str = str + "\nAdvisories:\n  " + strings.Join(br.Advisories, "  \n")
	}
	return fmt.Sprintf("%s\nElapsed: %v", str, br.Elapsed)
}
