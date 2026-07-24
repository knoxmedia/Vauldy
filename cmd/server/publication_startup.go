package main

import (
	"context"
	"fmt"
)

type publicationV2StartupHooks struct {
	Preflight           func(context.Context) ([]string, error)
	RecoverArtifacts    func(context.Context) error
	ReplaceAndAggregate func(context.Context) error
	StartClaimers       func()
}

// PreparePublicationV2Startup is the single fail-closed gate before publication claimers or scan sources start.
func PreparePublicationV2Startup(ctx context.Context, hooks publicationV2StartupHooks) ([]string, error) {
	if hooks.Preflight == nil || hooks.RecoverArtifacts == nil || hooks.ReplaceAndAggregate == nil || hooks.StartClaimers == nil {
		return nil, fmt.Errorf("publication v2 startup: incomplete lifecycle hooks")
	}
	warnings, err := hooks.Preflight(ctx)
	if err != nil {
		return nil, fmt.Errorf("publication v2 startup preflight: %w", err)
	}
	if err = hooks.RecoverArtifacts(ctx); err != nil {
		return nil, fmt.Errorf("publication v2 artifact recovery: %w", err)
	}
	if err = hooks.ReplaceAndAggregate(ctx); err != nil {
		return nil, fmt.Errorf("publication v2 reconciliation: %w", err)
	}
	hooks.StartClaimers()
	return warnings, nil
}
