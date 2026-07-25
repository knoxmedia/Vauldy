package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestStartupPublicationV2Order(t *testing.T) {
	var got []string
	phase := func(name string) func(context.Context) error {
		return func(context.Context) error { got = append(got, name); return nil }
	}
	hooks := publicationV2StartupHooks{
		RecoverArtifacts: phase("recover_artifacts"), RecoverLeases: phase("recover_leases"), ReplaceActiveV1: phase("replace_v1"), ValidateAggregateV2: phase("validate_aggregate_v2"),
		Preflight:     func(context.Context) ([]string, error) { got = append(got, "preflight"); return nil, nil },
		StartClaimers: func() { got = append(got, "claimers") }, StartSubmissionSources: func() { got = append(got, "submissions") },
	}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err != nil {
		t.Fatal(err)
	}
	want := []string{"recover_artifacts", "recover_leases", "replace_v1", "validate_aggregate_v2", "preflight", "claimers", "submissions"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v want=%v", got, want)
	}
}

func TestStartupPublicationV2FailureDoesNotInvokeClaimer(t *testing.T) {
	claimed := false
	ok := func(context.Context) error { return nil }
	hooks := publicationV2StartupHooks{RecoverArtifacts: ok, RecoverLeases: ok, ReplaceActiveV1: ok, ValidateAggregateV2: ok,
		Preflight:     func(context.Context) ([]string, error) { return nil, errors.New("fatal preflight") },
		StartClaimers: func() { claimed = true }, StartSubmissionSources: func() { t.Fatal("submission source invoked") },
	}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err == nil {
		t.Fatal("expected failure")
	}
	if claimed {
		t.Fatal("claimer callback invoked")
	}
}

func TestStartupPublicationV2ProbeFailurePreventsClaimersAndSources(t *testing.T) {
	claimed, sourced := false, false
	ok := func(context.Context) error { return nil }
	hooks := publicationV2StartupHooks{RecoverArtifacts: ok, RecoverLeases: ok, ReplaceActiveV1: ok, ValidateAggregateV2: ok, Preflight: func(context.Context) ([]string, error) { return nil, errors.New("artifact root read only") }, StartClaimers: func() { claimed = true }, StartSubmissionSources: func() { sourced = true }}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err == nil {
		t.Fatal("expected failure")
	}
	if claimed || sourced {
		t.Fatalf("claimed=%v sourced=%v", claimed, sourced)
	}
}
