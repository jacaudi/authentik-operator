package v1alpha1

import "testing"

func TestEffectiveDeletionPolicy(t *testing.T) {
	cases := []struct {
		name string
		mp   ManagementPolicy
		spec DeletionPolicy
		prov Provenance
		want DeletionPolicy
	}{
		{"created defaults to delete", ManagementPolicyCreate, "", ProvenanceCreated, DeletionPolicyDelete},
		{"adopted defaults to retain", ManagementPolicyAdopt, "", ProvenanceAdopted, DeletionPolicyRetain},
		{"undecided defaults to retain", ManagementPolicyCreate, "", "", DeletionPolicyRetain},
		{"explicit retain on created", ManagementPolicyCreate, DeletionPolicyRetain, ProvenanceCreated, DeletionPolicyRetain},
		{"explicit delete on adopted", ManagementPolicyAdopt, DeletionPolicyDelete, ProvenanceAdopted, DeletionPolicyDelete},
		{"explicit delete on undecided wins", ManagementPolicyCreate, DeletionPolicyDelete, "", DeletionPolicyDelete},
		{"unset policy behaves as create", "", "", ProvenanceCreated, DeletionPolicyDelete},
		{"read only never deletes a created object", ManagementPolicyReadOnly, "", ProvenanceCreated, DeletionPolicyRetain},
		{"read only never deletes even when delete slipped through", ManagementPolicyReadOnly, DeletionPolicyDelete,
			ProvenanceAdopted, DeletionPolicyRetain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EffectiveDeletionPolicy(c.mp, c.spec, c.prov); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestManagementPolicyCapabilities(t *testing.T) {
	cases := []struct {
		policy                ManagementPolicy
		wantAdopt, wantCreate bool
	}{
		{"", false, true},
		{ManagementPolicyCreate, false, true},
		{ManagementPolicyAdopt, true, false},
		{ManagementPolicyAdoptOrCreate, true, true},
		{ManagementPolicyReadOnly, true, false},
	}
	for _, c := range cases {
		t.Run(string(c.policy), func(t *testing.T) {
			if got := c.policy.MayAdopt(); got != c.wantAdopt {
				t.Errorf("MayAdopt() = %t, want %t", got, c.wantAdopt)
			}
			if got := c.policy.MayCreate(); got != c.wantCreate {
				t.Errorf("MayCreate() = %t, want %t", got, c.wantCreate)
			}
		})
	}
}
