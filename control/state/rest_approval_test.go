package state

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bit-precise/agents/control/types"
)

// A success must describe the exact reviewed configuration even if an agent
// updates concurrently between the approval transaction and HTTP serialization.
func TestCheckedApprovalConcurrentConfigurationChanges(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t, true)
	for i := 0; i < 25; i++ {
		before, err := f.s.Agent(enrolled.Agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		expected := before.Desired
		next := expected
		next.Serve.Services = slices.Clone(expected.Serve.Services)
		next.Serve.Services[0].Origin = fmt.Sprintf("http://127.0.0.1:%d", 19000+i)
		start := make(chan struct{})
		type outcome struct {
			agent types.Agent
			err   error
		}
		approved := make(chan outcome, 1)
		updated := make(chan error, 1)
		go func() {
			<-start
			a, err := f.s.ApproveAgentExpected(f.admin.ID, enrolled.Agent.ID, expected)
			approved <- outcome{a, err}
		}()
		go func() { <-start; _, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, next); updated <- err }()
		close(start)
		result := <-approved
		if err := <-updated; err != nil {
			t.Fatal(err)
		}
		if result.err != nil {
			requireKind(t, result.err, types.KindConflict)
		} else if result.agent.PendingApproval || result.agent.Desired.Serve.Services[0].Origin != expected.Serve.Services[0].Origin || result.agent.Approval.Services["demo"] != expected.Serve.Services[0].Origin {
			t.Fatalf("approval success did not match reviewed config: %+v", result.agent)
		}
		current, err := f.s.Agent(enrolled.Agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := f.s.ApproveAgentExpected(f.admin.ID, enrolled.Agent.ID, current.Desired)
		if err != nil {
			t.Fatal(err)
		}
		if accepted.PendingApproval || accepted.Approval.Services["demo"] != next.Serve.Services[0].Origin {
			t.Fatal("latest configuration was not approved")
		}
	}
}
