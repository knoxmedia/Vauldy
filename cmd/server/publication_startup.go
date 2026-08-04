package main

import (
	"context"
	"fmt"
)

type publicationV2StartupHooks struct {
	RecoverArtifacts       func(context.Context) error
	RecoverLeases          func(context.Context) error
	ReplaceActiveV1        func(context.Context) error
	ValidateAggregateV2    func(context.Context) error
	Preflight              func(context.Context) ([]string, error)
	StartClaimers          func()
	StartSubmissionSources func()
}

// PreparePublicationV2Startup is the single fail-closed gate before publication claimers or scan sources start.
func PreparePublicationV2Startup(ctx context.Context, hooks publicationV2StartupHooks) ([]string, error) {
	if hooks.RecoverArtifacts == nil || hooks.RecoverLeases == nil || hooks.ReplaceActiveV1 == nil || hooks.ValidateAggregateV2 == nil || hooks.Preflight == nil || hooks.StartClaimers == nil || hooks.StartSubmissionSources == nil {
		return nil, fmt.Errorf("publication v2 startup: incomplete lifecycle hooks")
	}
	for _, phase := range []struct {
		name string
		run  func(context.Context) error
	}{{"artifact recovery", hooks.RecoverArtifacts}, {"lease recovery", hooks.RecoverLeases}, {"active v1 replacement", hooks.ReplaceActiveV1}, {"v2 validation/aggregation", hooks.ValidateAggregateV2}} {
		if err := phase.run(ctx); err != nil {
			return nil, fmt.Errorf("publication v2 startup %s: %w", phase.name, err)
		}
	}
	warnings, err := hooks.Preflight(ctx)
	if err != nil {
		return nil, fmt.Errorf("publication v2 startup preflight: %w", err)
	}
	hooks.StartClaimers()
	hooks.StartSubmissionSources()
	return warnings, nil
}
