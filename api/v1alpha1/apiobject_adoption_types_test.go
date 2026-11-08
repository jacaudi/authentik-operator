package v1alpha1

import "testing"

func TestEffectiveDeletionPolicy(t *testing.T) {
	cases := []struct {
		name string
		spec DeletionPolicy
		prov Provenance
		want DeletionPolicy
	}{
		{"created defaults to delete", "", ProvenanceCreated, DeletionPolicyDelete},
		{"adopted defaults to retain", "", ProvenanceAdopted, DeletionPolicyRetain},
		{"undecided defaults to retain", "", "", DeletionPolicyRetain},
		{"explicit retain on created", DeletionPolicyRetain, ProvenanceCreated, DeletionPolicyRetain},
		{"explicit delete on adopted", DeletionPolicyDelete, ProvenanceAdopted, DeletionPolicyDelete},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EffectiveDeletionPolicy(c.spec, c.prov); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
