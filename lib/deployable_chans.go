package sous

import (
	"fmt"
	"sync"
)

type (
	// A DeployableChans is a bundle of channels describing actions to take on a
	// cluster
	DeployableChans struct {
		Start, Stop, Stable chan *Deployable
		Update              chan *DeployablePair
		sync.WaitGroup
	}

	// A DeployablePair is a pair of deployables, describing a "before and after"
	// situation, where the Prior Deployable is the known state and the Post
	// Deployable is the desired state.
	DeployablePair struct {
		Prior, Post *Deployable
		name        DeployID
	}
)

// NewDeployableChans returns a new DeployableChans with channels buffered to
// size.
func NewDeployableChans(size ...int) *DeployableChans {
	var s int
	if len(size) > 0 {
		s = size[0]
	}
	return &DeployableChans{
		Start:  make(chan *Deployable, s),
		Stop:   make(chan *Deployable, s),
		Stable: make(chan *Deployable, s),
		Update: make(chan *DeployablePair, s),
	}
}

// GuardImage checks that a deployment is valid before deploying it.
func GuardImage(r Registry, d *Deployment) (*BuildArtifact, error) {
	if d.NumInstances == 0 {
		Log.Info.Printf("Deployment %q has 0 instances, skipping artifact check.", d.ID())
		return nil, nil
	}
	art, err := r.GetArtifact(d.SourceID)
	if err != nil {
		return nil, &MissingImageNameError{err}
	}
	for _, q := range art.Qualities {
		if q.Kind == "advisory" {
			if q.Name == "" {
				continue
			}
			advisoryIsValid := false
			var allowedAdvisories []string
			if d.Cluster == nil {
				return nil, fmt.Errorf("nil cluster on deployment %q", d)
			}
			allowedAdvisories = d.Cluster.AllowedAdvisories
			for _, aa := range allowedAdvisories {
				if aa == q.Name {
					advisoryIsValid = true
					break
				}
			}
			if !advisoryIsValid {
				return nil, &UnacceptableAdvisory{q, &d.SourceID}
			}
		}
	}
	return art, err
}

// ID returns the ID of this DeployablePair.
func (dp *DeployablePair) ID() DeployID {
	return dp.name
}

// ResolveNames resolves diffs.
func (dc *DeployableChans) ResolveNames(r Registry, diff *DiffChans, errs chan error) {
	dc.WaitGroup = sync.WaitGroup{}
	dc.Add(4)
	go func() { resolveCreates(r, diff.Created, dc.Start, errs); dc.Done() }()
	go func() { maybeResolveDeletes(r, diff.Deleted, dc.Stop, errs); dc.Done() }()
	go func() { maybeResolveRetains(r, diff.Retained, dc.Stable, errs); dc.Done() }()
	go func() { resolvePairs(r, diff.Modified, dc.Update, errs); dc.Done() }()
	go func() { dc.Wait(); close(errs) }()
}

func resolveCreates(r Registry, from chan *DeploymentPair, to chan *Deployable, errs chan error) {
	for dp := range from {
		dep := dp.Post
		Log.Vomit.Printf("Deployment processed, needs artifact: %#v", dep)

		da, err := resolveName(r, dep, dp.Status)
		if err != nil {
			Log.Info.Printf("Unable to create new deployment %q: %s", dep.ID(), err)
			Log.Debug.Printf("Failed create deployment %q: % #v", dep.ID(), dep)
			errs <- err
			continue
		}

		if da.BuildArtifact == nil {
			Log.Info.Printf("Unable to create new deployment %q: no artifact for SourceID %q", dep.ID(), dep.SourceID)
			Log.Debug.Printf("Failed create deployment %q: % #v", dep.ID(), dep)
			continue
		}
		to <- da
	}
	close(to)
}

func maybeResolveRetains(r Registry, from chan *DeploymentPair, to chan *Deployable, errs chan error) {
	for dp := range from {
		da := maybeResolveSingle(r, dp.Post, dp.Status)
		to <- da
	}
	close(to)
}

func maybeResolveDeletes(r Registry, from chan *DeploymentPair, to chan *Deployable, errs chan error) {
	for dp := range from {
		da := maybeResolveSingle(r, dp.Prior, dp.Status)
		to <- da
	}
	close(to)
}

func maybeResolveSingle(r Registry, dep *Deployment, stat DeployStatus) *Deployable {
	Log.Vomit.Printf("Deployment processed w/o needing artifact: %#v", dep)
	da, err := resolveName(r, dep, stat)
	if err != nil {
		Log.Debug.Printf("Error resolving stopped or stable deployment (proceeding anyway): %#v: %#v", dep, err)
	}
	return da
}

func resolvePairs(r Registry, from chan *DeploymentPair, to chan *DeployablePair, errs chan error) {
	for depPair := range from {
		Log.Vomit.Printf("Pair of deployments processed, needs artifact: %#v", depPair)
		d, err := resolvePair(r, depPair)
		if err != nil {
			Log.Info.Printf("Unable to modify deployment %q: %s", depPair.Post, err)
			Log.Debug.Printf("Failed modify deployment %q: % #v", depPair.ID(), depPair.Post)
			errs <- err
			continue
		}
		if d.Post.BuildArtifact == nil {
			Log.Info.Printf("Unable to modify deployment %q: no artifact for SourceID %q", depPair.ID(), depPair.Post.SourceID)
			Log.Debug.Printf("Failed modify deployment %q: % #v", depPair.ID(), depPair.Post)
			continue
		}
		to <- d
	}
	close(to)
}

func resolveName(r Registry, dep *Deployment, stat DeployStatus) (*Deployable, error) {
	d := &Deployable{Deployment: dep, Status: stat}
	art, err := GuardImage(r, dep)
	if err == nil {
		d.BuildArtifact = art
	}
	return d, err
}

func resolvePair(r Registry, depPair *DeploymentPair) (*DeployablePair, error) {
	prior, _ := resolveName(r, depPair.Prior, depPair.Status)
	post, err := resolveName(r, depPair.Post, depPair.Status)

	return &DeployablePair{name: depPair.name, Prior: prior, Post: post}, err
}
