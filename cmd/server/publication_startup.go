package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

type publicationV2StartupHooks struct {
	RecoverArtifacts       func(context.Context) error
	RecoverLeases          func(context.Context) error
	RecoverReservations    func(context.Context) error
	ReplaceActiveV1        func(context.Context) error
	ValidateAggregateV2    func(context.Context) error
	Preflight              func(context.Context) ([]string, error)
	StartClaimers          func()
	StartSubmissionSources func()
}

// PreparePublicationV2Startup is the single fail-closed gate before publication claimers or scan sources start.
func PreparePublicationV2Startup(ctx context.Context, hooks publicationV2StartupHooks) ([]string, error) {
	if hooks.RecoverArtifacts == nil || hooks.RecoverLeases == nil || hooks.RecoverReservations == nil || hooks.ReplaceActiveV1 == nil || hooks.ValidateAggregateV2 == nil || hooks.Preflight == nil || hooks.StartClaimers == nil || hooks.StartSubmissionSources == nil {
		return nil, fmt.Errorf("publication v2 startup: incomplete lifecycle hooks")
	}
	for _, phase := range []struct {
		name string
		run  func(context.Context) error
	}{{"artifact recovery", hooks.RecoverArtifacts}, {"lease recovery", hooks.RecoverLeases}, {"reservation reconciliation", hooks.RecoverReservations}, {"active v1 replacement", hooks.ReplaceActiveV1}, {"v2 validation/aggregation", hooks.ValidateAggregateV2}} {
		start := time.Now()
		log.Printf("publication v2 startup: %s starting", phase.name)
		if err := phase.run(ctx); err != nil {
			return nil, fmt.Errorf("publication v2 startup %s: %w", phase.name, err)
		}
		log.Printf("publication v2 startup: %s completed in %s", phase.name, time.Since(start))
	}
	start := time.Now()
	warnings, err := hooks.Preflight(ctx)
	if err != nil {
		return nil, fmt.Errorf("publication v2 startup preflight: %w", err)
	}
	log.Printf("publication v2 startup: preflight completed in %s", time.Since(start))
	hooks.StartClaimers()
	hooks.StartSubmissionSources()
	return warnings, nil
}
