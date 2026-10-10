package digitalocean_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

const acceptedPolicyFloor = "d85f44c7573d66ae64ec003a94158480b24ac4df"
const acceptedPolicyFloorTree = "99b885d3d5e0eb34c9a59e99810760550140d28a"
const acceptedPolicyFloorInventorySHA = "28a832e8cc090a16c1895b75529460f6051efbbe1c5f8c3250dbc21d1f81f5bc"
const acceptedPolicyRetirementSHA = "846e2cc853920612a6dd4f6aa39b658433b202be09e55ef62b5008460d58e4fb"
const acceptedPolicyWorkflowSHA = "58a01a556fa391928e3bc8a56d517df770fae9479789bcd0758f0834421e8cc4"
const acceptedPolicyWorkflowContext = "ae860e737fff0acf1e835ff2a0539ef8ac0c187fb2f1e3f9042af16ab72fe545"
const acceptedPolicyRetirementHeadRef = "fix/policytool-go1272-promote-20261009"

type policyRetirement struct {
	Schema                       string `json:"schema"`
	Repository                   string `json:"repository"`
	AcceptedFloor                string `json:"acceptedFloor"`
	AcceptedFloorTree            string `json:"acceptedFloorTree"`
	AcceptedFloorInventorySHA256 string `json:"acceptedFloorInventorySHA256"`
	AcceptedFloorFiles           int    `json:"acceptedFloorFiles"`
	RetirementPullRequest        int    `json:"retirementPullRequest"`
	RetirementHeadRef            string `json:"retirementHeadRef"`
	WorkflowPath                 string `json:"workflowPath"`
	WorkflowSHA256               string `json:"workflowSHA256"`
	WorkflowContextSHA256        string `json:"workflowContextSHA256"`
	HistoricalBootstrapBase      string `json:"historicalBootstrapBase"`
	Transition                   string `json:"transition"`
	ReceiptSchema                string `json:"receiptSchema"`
}

func validatePolicyRetirement(data, workflow []byte) (policyRetirement, error) {
	var proposal policyRetirement
	if bootstrapHash(data) != acceptedPolicyRetirementSHA || bootstrapHash(workflow) != acceptedPolicyWorkflowSHA {
		return proposal, errors.New("reviewed retirement transition or unchanged CI bytes differ")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&proposal) != nil || d.Decode(new(any)) != io.EOF || proposal.Schema != "provider-policy-bootstrap-retirement/v1" || proposal.Repository != bootstrapRepo || proposal.AcceptedFloor != acceptedPolicyFloor || proposal.AcceptedFloorTree != acceptedPolicyFloorTree || proposal.AcceptedFloorInventorySHA256 != acceptedPolicyFloorInventorySHA || proposal.AcceptedFloorFiles != 257 || proposal.RetirementPullRequest != 203 || proposal.RetirementHeadRef != acceptedPolicyRetirementHeadRef || proposal.WorkflowPath != ".github/workflows/ci.yml" || proposal.WorkflowSHA256 != acceptedPolicyWorkflowSHA || proposal.WorkflowContextSHA256 != acceptedPolicyWorkflowContext || proposal.HistoricalBootstrapBase != bootstrapBase || proposal.ReceiptSchema != "provider-actual-accepted-policy/v3" {
		return proposal, errors.New("reviewed retirement transition identity differs")
	}
	return proposal, nil
}

func acceptedPolicyRetirementProposal(t *testing.T) policyRetirement {
	t.Helper()
	data, err := bootstrapRegularFile("testdata/provider-policy-bootstrap-retirement.json", 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := bootstrapRegularFile(".github/workflows/ci.yml", 128*1024)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := validatePolicyRetirement(data, workflow)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func acceptedPolicyCommit(value string) bool {
	return bootstrapCommitPattern.MatchString(value) && value != strings.Repeat("0", 40)
}

// Retirement removes the one-shot event gate, not the accepted-policy proof.
// Metadata selects a real prior accepted base; Git inventory and ancestry are
// independently checked before its checker can be built. Candidate policy is data.
func bindAcceptedPolicyEvent(name, workflowRef, workflowSHA, checkout, githubSHA, preparationSHA string, event bootstrapEvent) (bootstrapBinding, error) {
	b := bootstrapBinding{WorkflowSHA: workflowSHA, Checkout: checkout}
	if event.Repository.FullName != bootstrapRepo || !acceptedPolicyCommit(workflowSHA) || workflowSHA != checkout || githubSHA != checkout || preparationSHA != "" {
		return b, errors.New("accepted-policy proof requires exact hosted checkout/workflow identities without preparation authority")
	}
	switch name {
	case "pull_request":
		pr := event.PullRequest
		if event.Number <= 0 || workflowRef != fmt.Sprintf("%s/.github/workflows/ci.yml@refs/pull/%d/merge", bootstrapRepo, event.Number) || pr.Base.Ref != "main" || pr.Base.Repo.FullName != bootstrapRepo || pr.Head.Repo.FullName != bootstrapRepo || !acceptedPolicyCommit(pr.Base.SHA) || !acceptedPolicyCommit(pr.Head.SHA) || pr.Base.SHA == pr.Head.SHA || checkout == pr.Head.SHA || checkout == pr.Base.SHA {
			return b, errors.New("accepted-policy PR requires the actual main base and distinct same-repository candidate/synthetic merge")
		}
		if event.Number == 203 && (pr.Base.SHA != acceptedPolicyFloor || pr.Head.Ref != acceptedPolicyRetirementHeadRef) {
			return b, errors.New("retirement PR203 must bind the exact accepted cutover and reviewed candidate ref")
		}
		b.Mode, b.Base, b.Candidate, b.Merge = "accepted-policy-pr", pr.Base.SHA, pr.Head.SHA, checkout
	case "push":
		if event.Ref != "refs/heads/main" || workflowRef != bootstrapRepo+"/.github/workflows/ci.yml@refs/heads/main" || !acceptedPolicyCommit(event.Before) || event.After != checkout || event.Before == event.After {
			return b, errors.New("accepted-policy main proof requires distinct exact prior authority and actual main checkout")
		}
		b.Mode, b.Base, b.Candidate = "accepted-policy-main", event.Before, checkout
	default:
		return b, errors.New("accepted-policy proof has no local, helper, tag or missing-policy event fallback")
	}
	return b, nil
}

func verifyAcceptedPolicyParents(binding bootstrapBinding, parents []string) error {
	if binding.Mode == "accepted-policy-pr" {
		if len(parents) != 2 || parents[0] != binding.Base || parents[1] != binding.Candidate || binding.Merge != binding.Checkout {
			return errors.New("actual PR merge parents do not exactly bind accepted base and candidate in order")
		}
		return nil
	}
	if binding.Mode != "accepted-policy-main" || len(parents) == 0 || parents[0] != binding.Base || binding.Candidate != binding.Checkout || binding.Merge != "" {
		return errors.New("actual main checkout does not have the event-selected prior authority as first parent")
	}
	return nil
}

func TestAcceptedPolicyRetirementEventBindings(t *testing.T) {
	candidate, merge := strings.Repeat("b", 40), strings.Repeat("c", 40)
	pr := bootstrapEvent{Number: 203}
	pr.Repository.FullName = bootstrapRepo
	pr.PullRequest.Head.SHA, pr.PullRequest.Head.Ref, pr.PullRequest.Head.Repo.FullName = candidate, acceptedPolicyRetirementHeadRef, bootstrapRepo
	pr.PullRequest.Base.SHA, pr.PullRequest.Base.Ref, pr.PullRequest.Base.Repo.FullName = acceptedPolicyFloor, "main", bootstrapRepo
	workflow := bootstrapRepo + "/.github/workflows/ci.yml@refs/pull/203/merge"
	b, err := bindAcceptedPolicyEvent("pull_request", workflow, merge, merge, merge, "", pr)
	if err != nil || b.Base != acceptedPolicyFloor || b.Candidate != candidate || b.Merge != merge {
		t.Fatal("reviewed retirement candidate/base binding was rejected")
	}
	if verifyAcceptedPolicyParents(b, []string{acceptedPolicyFloor, candidate}) != nil {
		t.Fatal("exact ordered actual merge parents were rejected")
	}
	for _, parents := range [][]string{nil, {acceptedPolicyFloor}, {candidate, acceptedPolicyFloor}, {acceptedPolicyFloor, merge}, {acceptedPolicyFloor, candidate, merge}} {
		if verifyAcceptedPolicyParents(b, parents) == nil {
			t.Fatal("wrong, absent, reversed or additional actual merge parents were accepted")
		}
	}
	for _, mutate := range []func(*bootstrapEvent){
		func(e *bootstrapEvent) { e.Number = 0 },
		func(e *bootstrapEvent) { e.Number = -1 },
		func(e *bootstrapEvent) { e.Number = 201 },
		func(e *bootstrapEvent) { e.Repository.FullName = "attacker/repo" },
		func(e *bootstrapEvent) { e.PullRequest.Base.Repo.FullName = "attacker/repo" },
		func(e *bootstrapEvent) { e.PullRequest.Base.Ref = "other" },
		func(e *bootstrapEvent) { e.PullRequest.Base.SHA = candidate },
		func(e *bootstrapEvent) { e.PullRequest.Head.Repo.FullName = "attacker/repo" },
		func(e *bootstrapEvent) { e.PullRequest.Head.Ref = "other" },
		func(e *bootstrapEvent) { e.PullRequest.Head.SHA = "" },
		func(e *bootstrapEvent) { e.PullRequest.Head.SHA = strings.Repeat("0", 40) },
		func(e *bootstrapEvent) { e.PullRequest.Head.SHA = merge },
	} {
		changed := pr
		mutate(&changed)
		if _, err := bindAcceptedPolicyEvent("pull_request", workflow, merge, merge, merge, "", changed); err == nil {
			t.Fatal("changed retirement PR metadata was admitted")
		}
	}
	for _, values := range [][4]string{{workflow + "wrong", merge, merge, ""}, {workflow, candidate, merge, ""}, {workflow, merge, candidate, ""}, {workflow, merge, merge, candidate}} {
		if _, err := bindAcceptedPolicyEvent("pull_request", values[0], values[1], merge, values[2], values[3], pr); err == nil {
			t.Fatal("wrong workflow, checkout SHA or preparation authority was admitted")
		}
	}
	// Later PRs still need their real main base; retirement is not another
	// finite PR203 gate. Runtime Git checks separately require floor ancestry.
	later := pr
	later.Number, later.PullRequest.Base.SHA = 204, strings.Repeat("d", 40)
	later.PullRequest.Head.Ref = "fix/later"
	if _, err := bindAcceptedPolicyEvent("pull_request", bootstrapRepo+"/.github/workflows/ci.yml@refs/pull/204/merge", merge, merge, merge, "", later); err != nil {
		t.Fatal("later real accepted-base event was rejected")
	}
	main := bootstrapEvent{Ref: "refs/heads/main", Before: acceptedPolicyFloor, After: candidate}
	main.Repository.FullName = bootstrapRepo
	workflow = bootstrapRepo + "/.github/workflows/ci.yml@refs/heads/main"
	b, err = bindAcceptedPolicyEvent("push", workflow, candidate, candidate, candidate, "", main)
	if err != nil || b.Base != acceptedPolicyFloor || b.Candidate != candidate || b.Merge != "" || verifyAcceptedPolicyParents(b, []string{acceptedPolicyFloor, merge}) != nil {
		t.Fatal("actual accepted-main before/after binding was rejected")
	}
	if verifyAcceptedPolicyParents(b, nil) == nil || verifyAcceptedPolicyParents(b, []string{merge, acceptedPolicyFloor}) == nil {
		t.Fatal("wrong main prior-authority parent was admitted")
	}
	for _, mutate := range []func(*bootstrapEvent){
		func(e *bootstrapEvent) { e.Before = strings.Repeat("0", 40) },
		func(e *bootstrapEvent) { e.Before = "" },
		func(e *bootstrapEvent) { e.Before = candidate },
		func(e *bootstrapEvent) { e.After = merge },
		func(e *bootstrapEvent) { e.Ref = "refs/heads/other" },
	} {
		changed := main
		mutate(&changed)
		if _, err := bindAcceptedPolicyEvent("push", workflow, candidate, candidate, candidate, "", changed); err == nil {
			t.Fatal("changed actual main metadata was admitted")
		}
	}
	for _, name := range []string{"workflow_dispatch", "pull_request_target", "unknown", ""} {
		if _, err := bindAcceptedPolicyEvent(name, workflow, candidate, candidate, candidate, "", main); err == nil {
			t.Fatal("unsupported event fallback was admitted")
		}
	}
}

func TestAcceptedPolicyRetirementRejectsChangedProposalOrCI(t *testing.T) {
	data, err := os.ReadFile("testdata/provider-policy-bootstrap-retirement.json")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validatePolicyRetirement(data, workflow); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePolicyRetirement(append(append([]byte(nil), data...), '\n'), workflow); err == nil {
		t.Fatal("changed reviewed retirement transition was admitted")
	}
	if _, err := validatePolicyRetirement(data, append(append([]byte(nil), workflow...), '\n')); err == nil {
		t.Fatal("changed live CI authority was admitted")
	}
}
