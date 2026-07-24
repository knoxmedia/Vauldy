package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestStartupPublicationV2Order(t *testing.T) {
	var got []string
	hooks := publicationV2StartupHooks{
		Preflight:           func(context.Context) ([]string, error) { got = append(got, "preflight"); return nil, nil },
		RecoverArtifacts:    func(context.Context) error { got = append(got, "artifact_recovery"); return nil },
		ReplaceAndAggregate: func(context.Context) error { got = append(got, "replace_aggregate"); return nil },
		StartClaimers:       func() { got = append(got, "claimers") },
	}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"preflight", "artifact_recovery", "replace_aggregate", "claimers"}) {
		t.Fatalf("order=%v", got)
	}
}

func TestStartupPublicationV2FailureDoesNotInvokeClaimer(t *testing.T) {
	claimed := false
	hooks := publicationV2StartupHooks{
		Preflight:           func(context.Context) ([]string, error) { return nil, errors.New("fatal preflight") },
		RecoverArtifacts:    func(context.Context) error { t.Fatal("recovery after failed preflight"); return nil },
		ReplaceAndAggregate: func(context.Context) error { t.Fatal("reconcile after failed preflight"); return nil },
		StartClaimers:       func() { claimed = true },
	}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err == nil {
		t.Fatal("expected failure")
	}
	if claimed {
		t.Fatal("claimer callback invoked")
	}
}
