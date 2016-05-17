/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mungers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/kubernetes/pkg/util"

	github_util "k8s.io/contrib/mungegithub/github"
	github_test "k8s.io/contrib/mungegithub/github/testing"
	"k8s.io/contrib/mungegithub/mungers/e2e"
	fake_e2e "k8s.io/contrib/mungegithub/mungers/e2e/fake"
	"k8s.io/contrib/mungegithub/mungers/jenkins"
	"k8s.io/contrib/test-utils/utils"

	"github.com/golang/glog"
	"github.com/google/go-github/github"
)

var (
	_ = fmt.Printf
	_ = glog.Errorf
)

func stringPtr(val string) *string { return &val }
func boolPtr(val bool) *bool       { return &val }
func intPtr(val int) *int          { return &val }

const noWhitelistUser = "UserNotInWhitelist"
const whitelistUser = "WhitelistUser"

func ValidPR() *github.PullRequest {
	return github_test.PullRequest(whitelistUser, false, true, true)
}

func UnMergeablePR() *github.PullRequest {
	return github_test.PullRequest(whitelistUser, false, true, false)
}

func UndeterminedMergeablePR() *github.PullRequest {
	return github_test.PullRequest(whitelistUser, false, false, false)
}

func NonWhitelistUserPR() *github.PullRequest {
	return github_test.PullRequest(noWhitelistUser, false, true, true)
}

func BareIssue() *github.Issue {
	return github_test.Issue(whitelistUser, 1, []string{}, true)
}

func NoOKToMergeIssue() *github.Issue {
	return github_test.Issue(whitelistUser, 1, []string{claYesLabel, lgtmLabel}, true)
}

func DoNotMergeIssue() *github.Issue {
	return github_test.Issue(whitelistUser, 1, []string{claYesLabel, lgtmLabel, doNotMergeLabel}, true)
}

func NoCLAIssue() *github.Issue {
	return github_test.Issue(whitelistUser, 1, []string{lgtmLabel, okToMergeLabel}, true)
}

func NoLGTMIssue() *github.Issue {
	return github_test.Issue(whitelistUser, 1, []string{claYesLabel, okToMergeLabel}, true)
}

func UserNotInWhitelistNoOKToMergeIssue() *github.Issue {
	return github_test.Issue(noWhitelistUser, 1, []string{claYesLabel, lgtmLabel}, true)
}

func UserNotInWhitelistOKToMergeIssue() *github.Issue {
	return github_test.Issue(noWhitelistUser, 1, []string{lgtmLabel, claYesLabel, okToMergeLabel}, true)
}

func DontRequireGithubE2EIssue() *github.Issue {
	return github_test.Issue(whitelistUser, 1, []string{claYesLabel, lgtmLabel, e2eNotRequiredLabel}, true)
}

func OldLGTMEvents() []github.IssueEvent {
	return github_test.Events([]github_test.LabelTime{
		{"bob", lgtmLabel, 6},
		{"bob", lgtmLabel, 7},
		{"bob", lgtmLabel, 8},
	})
}

func NewLGTMEvents() []github.IssueEvent {
	return github_test.Events([]github_test.LabelTime{
		{"bob", lgtmLabel, 10},
		{"bob", lgtmLabel, 11},
		{"bob", lgtmLabel, 12},
	})
}

func OverlappingLGTMEvents() []github.IssueEvent {
	return github_test.Events([]github_test.LabelTime{
		{"bob", lgtmLabel, 8},
		{"bob", lgtmLabel, 9},
		{"bob", lgtmLabel, 10},
	})
}

// Commits returns a slice of github.RepositoryCommit of len==3 which
// happened at times 7, 8, 9
func Commits() []github.RepositoryCommit {
	return github_test.Commits(3, 7)
}

func SuccessStatus() *github.CombinedStatus {
	return github_test.Status("mysha", []string{travisContext, jenkinsUnitContext, jenkinsE2EContext}, nil, nil, nil)
}

func GithubE2EFailStatus() *github.CombinedStatus {
	return github_test.Status("mysha", []string{travisContext, jenkinsUnitContext}, []string{jenkinsE2EContext}, nil, nil)
}

func LastBuildNumber() int {
	return 42
}

func SuccessGCS() utils.FinishedFile {
	return utils.FinishedFile{
		Result:    "SUCCESS",
		Timestamp: uint64(time.Now().Unix()),
	}
}

func FailGCS() utils.FinishedFile {
	return utils.FinishedFile{
		Result:    "FAILURE",
		Timestamp: uint64(time.Now().Unix()),
	}
}

func SuccessJenkins() jenkins.Job {
	return jenkins.Job{
		Result: "SUCCESS",
	}
}

func FailJenkins() jenkins.Job {
	return jenkins.Job{
		Result: "FAILED",
	}
}

func getJUnit(testsNo int, failuresNo int) []byte {
	return []byte(fmt.Sprintf("%v\n<testsuite tests=\"%v\" failures=\"%v\" time=\"1234\">\n</testsuite>",
		e2e.ExpectedXMLHeader, testsNo, failuresNo))
}

func getTestSQ(startThreads bool, config *github_util.Config, server *httptest.Server) *SubmitQueue {
	sq := new(SubmitQueue)
	sq.RequiredStatusContexts = []string{jenkinsUnitContext}
	sq.E2EStatusContext = jenkinsE2EContext
	sq.UnitStatusContext = jenkinsUnitContext
	if server != nil {
		sq.JenkinsHost = server.URL
	}
	sq.JobNames = []string{"foo"}
	sq.WeakStableJobNames = []string{"bar"}
	sq.githubE2EQueue = map[int]*github_util.MungeObject{}
	sq.githubE2EPollTime = 50 * time.Millisecond

	sq.clock = util.NewFakeClock(time.Time{})
	sq.lastMergeTime = sq.clock.Now()
	sq.lastE2EStable = true
	sq.prStatus = map[string]submitStatus{}
	sq.lastPRStatus = map[string]submitStatus{}

	sq.health.StartTime = sq.clock.Now()
	sq.healthHistory = make([]healthRecord, 0)

	sq.e2e = &fake_e2e.FakeE2ETester{
		JobNames:           sq.JobNames,
		WeakStableJobNames: sq.WeakStableJobNames,
	}

	if startThreads {
		sq.internalInitialize(config, nil, server.URL)
		sq.EachLoop()
		sq.userWhitelist.Insert(whitelistUser)
	}
	return sq
}

func TestQueueOrder(t *testing.T) {
	tests := []struct {
		name     string
		issues   []github.Issue
		expected []int
	}{
		{
			name: "Just prNum",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, nil, true),
				*github_test.Issue(whitelistUser, 3, nil, true),
				*github_test.Issue(whitelistUser, 4, nil, true),
				*github_test.Issue(whitelistUser, 5, nil, true),
			},
			expected: []int{2, 3, 4, 5},
		},
		{
			name: "With a priority label",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, []string{"priority/P1"}, true),
				*github_test.Issue(whitelistUser, 3, []string{"priority/P1"}, true),
				*github_test.Issue(whitelistUser, 4, []string{"priority/P0"}, true),
				*github_test.Issue(whitelistUser, 5, nil, true),
			},
			expected: []int{4, 2, 3, 5},
		},
		{
			name: "With two priority labels",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, []string{"priority/P1", "priority/P0"}, true),
				*github_test.Issue(whitelistUser, 3, []string{"priority/P1"}, true),
				*github_test.Issue(whitelistUser, 4, []string{"priority/P0"}, true),
				*github_test.Issue(whitelistUser, 5, nil, true),
			},
			expected: []int{2, 4, 3, 5},
		},
		{
			name: "With unrelated labels",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, []string{"priority/P1", "priority/P0"}, true),
				*github_test.Issue(whitelistUser, 3, []string{"priority/P1", "kind/design"}, true),
				*github_test.Issue(whitelistUser, 4, []string{"priority/P0"}, true),
				*github_test.Issue(whitelistUser, 5, []string{lgtmLabel, "kind/new-api"}, true),
			},
			expected: []int{2, 4, 3, 5},
		},
		{
			name: "With invalid priority label",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, []string{"priority/P1", "priority/P0"}, true),
				*github_test.Issue(whitelistUser, 3, []string{"priority/P1", "kind/design", "priority/high"}, true),
				*github_test.Issue(whitelistUser, 4, []string{"priority/P0", "priorty/bob"}, true),
				*github_test.Issue(whitelistUser, 5, nil, true),
			},
			expected: []int{2, 4, 3, 5},
		},
		{
			name: "Unlabeled counts as P3",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, nil, true),
				*github_test.Issue(whitelistUser, 3, []string{"priority/P3"}, true),
				*github_test.Issue(whitelistUser, 4, []string{"priority/P2"}, true),
				*github_test.Issue(whitelistUser, 5, nil, true),
			},
			expected: []int{4, 2, 3, 5},
		},
		{
			name: "e2eNotRequiredLabel counts as P-negative 1",
			issues: []github.Issue{
				*github_test.Issue(whitelistUser, 2, nil, true),
				*github_test.Issue(whitelistUser, 3, []string{"priority/P3"}, true),
				*github_test.Issue(whitelistUser, 4, []string{"priority/P2"}, true),
				*github_test.Issue(whitelistUser, 5, nil, true),
				*github_test.Issue(whitelistUser, 6, []string{"priority/P3", e2eNotRequiredLabel}, true),
			},
			expected: []int{6, 4, 2, 3, 5},
		},
	}
	for testNum, test := range tests {
		config := &github_util.Config{}
		client, server, mux := github_test.InitServer(t, nil, nil, nil, nil, nil)
		config.Org = "o"
		config.Project = "r"
		config.SetClient(client)
		sq := getTestSQ(false, config, server)
		for i := range test.issues {
			issue := &test.issues[i]
			github_test.ServeIssue(t, mux, issue)

			issueNum := *issue.Number
			obj, err := config.GetObject(issueNum)
			if err != nil {
				t.Fatalf("%d:%q unable to get issue: %v", testNum, test.name, err)
			}
			sq.githubE2EQueue[issueNum] = obj
		}
		actual := sq.orderedE2EQueue()
		if len(actual) != len(test.expected) {
			t.Fatalf("%d:%q len(actual):%v != len(expected):%v", testNum, test.name, actual, test.expected)
		}
		for i, a := range actual {
			e := test.expected[i]
			if a != e {
				t.Errorf("%d:%q a[%d]:%d != e[%d]:%d", testNum, test.name, i, a, i, e)
			}
		}
		server.Close()
	}
}

func TestValidateLGTMAfterPush(t *testing.T) {
	tests := []struct {
		issueEvents []github.IssueEvent
		commits     []github.RepositoryCommit
		shouldPass  bool
	}{
		{
			issueEvents: NewLGTMEvents(), // Label >= time.Unix(10)
			commits:     Commits(),       // Modified at time.Unix(7), 8, and 9
			shouldPass:  true,
		},
		{
			issueEvents: OldLGTMEvents(), // Label <= time.Unix(8)
			commits:     Commits(),       // Modified at time.Unix(7), 8, and 9
			shouldPass:  false,
		},
		{
			issueEvents: OverlappingLGTMEvents(), // Labeled at 8, 9, and 10
			commits:     Commits(),               // Modified at time.Unix(7), 8, and 9
			shouldPass:  true,
		},
	}
	for testNum, test := range tests {
		config := &github_util.Config{}
		client, server, _ := github_test.InitServer(t, nil, nil, test.issueEvents, test.commits, nil)
		config.Org = "o"
		config.Project = "r"
		config.SetClient(client)

		obj := github_util.TestObject(config, BareIssue(), nil, nil, nil)

		if _, err := obj.GetCommits(); err != nil {
			t.Errorf("Unexpected error getting filled commits: %v", err)
		}

		if _, err := obj.GetEvents(); err != nil {
			t.Errorf("Unexpected error getting events commits: %v", err)
		}

		lastModifiedTime := obj.LastModifiedTime()
		lgtmTime := obj.LabelTime(lgtmLabel)

		if lastModifiedTime == nil || lgtmTime == nil {
			t.Errorf("unexpected lastModifiedTime or lgtmTime == nil")
		}

		ok := !lastModifiedTime.After(*lgtmTime)

		if ok != test.shouldPass {
			t.Errorf("%d: expected: %v, saw: %v", testNum, test.shouldPass, ok)
		}
		server.Close()
	}
}

func setStatus(status *github.RepoStatus, success bool) {
	if success {
		status.State = stringPtr("success")
	} else {
		status.State = stringPtr("failure")
	}
}

func addStatus(context string, success bool, ciStatus *github.CombinedStatus) {
	status := github.RepoStatus{
		Context: stringPtr(context),
	}
	setStatus(&status, success)
	ciStatus.Statuses = append(ciStatus.Statuses, status)
}

// fakeRunGithubE2ESuccess imitates jenkins running
func fakeRunGithubE2ESuccess(ciStatus *github.CombinedStatus, e2ePass, unitPass bool) {
	ciStatus.State = stringPtr("pending")
	for id := range ciStatus.Statuses {
		status := &ciStatus.Statuses[id]
		if *status.Context == jenkinsE2EContext || *status.Context == jenkinsUnitContext {
			status.State = stringPtr("pending")
		}
	}
	// short sleep like the test is running
	time.Sleep(500 * time.Millisecond)
	if e2ePass && unitPass {
		ciStatus.State = stringPtr("success")
	}
	foundE2E := false
	foundUnit := false
	for id := range ciStatus.Statuses {
		status := &ciStatus.Statuses[id]
		if *status.Context == jenkinsE2EContext {
			setStatus(status, e2ePass)
			foundE2E = true
		}
		if *status.Context == jenkinsUnitContext {
			setStatus(status, unitPass)
			foundUnit = true
		}
	}
	if !foundE2E {
		addStatus(jenkinsE2EContext, e2ePass, ciStatus)
	}
	if !foundUnit {
		addStatus(jenkinsUnitContext, unitPass, ciStatus)
	}
}

func TestSubmitQueue(t *testing.T) {
	runtime.GOMAXPROCS(runtime.NumCPU())

	tests := []struct {
		name             string // because when the fail, counting is hard
		pr               *github.PullRequest
		issue            *github.Issue
		commits          []github.RepositoryCommit
		events           []github.IssueEvent
		ciStatus         *github.CombinedStatus
		jenkinsJob       jenkins.Job
		lastBuildNumber  int
		gcsResult        utils.FinishedFile
		weakResults      map[int]utils.FinishedFile
		gcsJunit         map[string][]byte
		e2ePass          bool
		unitPass         bool
		mergeAfterQueued bool
		reason           string
		state            string // what the github status context should be for the PR HEAD
	}{
		// Should pass because the entire thing was run and good
		{
			name:            "Test1",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			ciStatus:        SuccessStatus(),
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			e2ePass:         true,
			unitPass:        true,
			reason:          merged,
			state:           "success",
		},
		// Should list as 'merged' but the merge should happen before it gets e2e tested
		// and we should bail early instead of waiting for a test that will never come.
		{
			name:            "Test2",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			events:          NewLGTMEvents(),
			commits:         Commits(),
			ciStatus:        SuccessStatus(),
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			// The test should never run, but if it does, make sure it fails
			mergeAfterQueued: true,
			reason:           merged,
			state:            "success",
		},
		// Should merge even though github ci failed because of dont-require-e2e
		{
			name:            "Test3",
			pr:              ValidPR(),
			issue:           DontRequireGithubE2EIssue(),
			ciStatus:        GithubE2EFailStatus(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			reason:          merged,
			state:           "success",
		},
		// Should merge even though user not in whitelist because has okToMergeLabel
		{
			name:            "Test4",
			pr:              ValidPR(),
			issue:           UserNotInWhitelistOKToMergeIssue(),
			ciStatus:        SuccessStatus(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			e2ePass:         true,
			unitPass:        true,
			reason:          merged,
			state:           "success",
		},
		// Fail because PR can't automatically merge
		{
			name:   "Test5",
			pr:     UnMergeablePR(),
			issue:  NoOKToMergeIssue(),
			reason: unmergeable,
			state:  "pending",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because we don't know if PR can automatically merge
		{
			name:   "Test6",
			pr:     UndeterminedMergeablePR(),
			issue:  NoOKToMergeIssue(),
			reason: undeterminedMergability,
			state:  "pending",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because the claYesLabel label was not applied
		{
			name:   "Test7",
			pr:     ValidPR(),
			issue:  NoCLAIssue(),
			reason: noCLA,
			state:  "pending",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because github CI tests have failed (or at least are not success)
		{
			name:   "Test8",
			pr:     NonWhitelistUserPR(),
			issue:  NoOKToMergeIssue(),
			reason: ciFailure,
			state:  "pending",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because the user is not in the whitelist and we don't have okToMergeLabel
		{
			name:     "Test9",
			pr:       ValidPR(),
			issue:    UserNotInWhitelistNoOKToMergeIssue(),
			ciStatus: SuccessStatus(),
			reason:   needsok,
			state:    "pending",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because missing LGTM label
		{
			name:     "Test10",
			pr:       ValidPR(),
			issue:    NoLGTMIssue(),
			ciStatus: SuccessStatus(),
			reason:   noLGTM,
			state:    "pending",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because we can't tell if LGTM was added before the last change
		{
			name:     "Test11",
			pr:       ValidPR(),
			issue:    NoOKToMergeIssue(),
			ciStatus: SuccessStatus(),
			reason:   unknown,
			state:    "failure",
			// To avoid false errors in logs
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
		},
		// Fail because LGTM was added before the last change
		{
			name:     "Test12",
			pr:       ValidPR(),
			issue:    NoOKToMergeIssue(),
			ciStatus: SuccessStatus(),
			events:   OldLGTMEvents(),
			commits:  Commits(), // Modified at time.Unix(7), 8, and 9
			reason:   lgtmEarly,
			state:    "pending",
		},
		// Fail because jenkins instances are failing (whole submit queue blocks)
		{
			name:            "Test13",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			ciStatus:        SuccessStatus(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			jenkinsJob:      FailJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       FailGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			reason:          ghE2EQueued,
			state:           "success",
		},
		// Fail because the second run of github e2e tests failed
		{
			name:            "Test14",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			ciStatus:        SuccessStatus(),
			events:          NewLGTMEvents(),
			commits:         Commits(),
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			reason:          ghE2EFailed,
			state:           "pending",
		},
		// When we check the reason it may be queued or it may already have failed.
		{
			name:            "Test15",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			ciStatus:        SuccessStatus(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			reason:          ghE2EQueued,
			// The state is unpredictable. When it goes on the queue it is success.
			// When it fails the build it is pending. So state depends on how far along
			// this were when we checked. Thus just don't check it...
			state: "",
		},
		// Fail because the second run of github e2e tests failed
		{
			name:            "Test16",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			ciStatus:        SuccessStatus(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			reason:          ghE2EFailed,
			state:           "pending",
		},
		{
			name:            "Fail because E2E pass, but unit test fail",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			ciStatus:        SuccessStatus(),
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			e2ePass:         true,
			unitPass:        false,
			reason:          ghE2EFailed,
			state:           "pending",
		},
		{
			name:            "Fail because E2E fail, but unit test pass",
			pr:              ValidPR(),
			issue:           NoOKToMergeIssue(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			ciStatus:        SuccessStatus(),
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			e2ePass:         false,
			unitPass:        true,
			reason:          ghE2EFailed,
			state:           "pending",
		},
		{
			name:            "Fail because doNotMerge label is present",
			pr:              ValidPR(),
			issue:           DoNotMergeIssue(),
			events:          NewLGTMEvents(),
			commits:         Commits(), // Modified at time.Unix(7), 8, and 9
			ciStatus:        SuccessStatus(),
			jenkinsJob:      SuccessJenkins(),
			lastBuildNumber: LastBuildNumber(),
			gcsResult:       SuccessGCS(),
			weakResults:     map[int]utils.FinishedFile{LastBuildNumber(): SuccessGCS()},
			e2ePass:         true,
			unitPass:        true,
			reason:          noMerge,
			state:           "pending",
		},
		// // Should pass even though last 'weakStable' build failed, as it wasn't "strong" failure
		// // and because previous two builds succeeded.
		// {
		// 	name:            "Test20",
		// 	pr:              ValidPR(),
		// 	issue:           NoOKToMergeIssue(),
		// 	events:          NewLGTMEvents(),
		// 	commits:         Commits(), // Modified at time.Unix(7), 8, and 9
		// 	ciStatus:        SuccessStatus(),
		// 	jenkinsJob:      SuccessJenkins(),
		// 	lastBuildNumber: LastBuildNumber(),
		// 	gcsResult:       SuccessGCS(),
		// 	weakResults: map[int]utils.FinishedFile{
		// 		LastBuildNumber():     FailGCS(),
		// 		LastBuildNumber() - 1: SuccessGCS(),
		// 		LastBuildNumber() - 2: SuccessGCS(),
		// 	},
		// 	gcsJunit: map[string][]byte{
		// 		"junit_01.xml": getJUnit(5, 0),
		// 		"junit_02.xml": getJUnit(6, 0),
		// 		"junit_03.xml": getJUnit(7, 0),
		// 	},
		// 	e2ePass:  true,
		// 	unitPass: true,
		// 	reason:   merged,
		// 	state:    "success",
		// },
		// // Should fail because the failure of the weakStable job is a strong failure.
		// {
		// 	name:            "Test21",
		// 	pr:              ValidPR(),
		// 	issue:           NoOKToMergeIssue(),
		// 	events:          NewLGTMEvents(),
		// 	commits:         Commits(), // Modified at time.Unix(7), 8, and 9
		// 	ciStatus:        SuccessStatus(),
		// 	jenkinsJob:      SuccessJenkins(),
		// 	lastBuildNumber: LastBuildNumber(),
		// 	gcsResult:       SuccessGCS(),
		// 	weakResults: map[int]utils.FinishedFile{
		// 		LastBuildNumber():     FailGCS(),
		// 		LastBuildNumber() - 1: SuccessGCS(),
		// 		LastBuildNumber() - 2: SuccessGCS(),
		// 	},
		// 	gcsJunit: map[string][]byte{
		// 		"junit_01.xml": getJUnit(5, 0),
		// 		"junit_02.xml": getJUnit(6, 1),
		// 		"junit_03.xml": getJUnit(7, 0),
		// 	},
		// 	e2ePass:  true,
		// 	unitPass: true,
		// 	reason:   e2eFailure,
		// 	state:    "success",
		// },
		// // Should fail even though weakStable job weakly failed, because there was another failure in
		// // previous two runs.
		// {
		// 	name:            "Test22",
		// 	pr:              ValidPR(),
		// 	issue:           NoOKToMergeIssue(),
		// 	events:          NewLGTMEvents(),
		// 	commits:         Commits(), // Modified at time.Unix(7), 8, and 9
		// 	ciStatus:        SuccessStatus(),
		// 	jenkinsJob:      SuccessJenkins(),
		// 	lastBuildNumber: LastBuildNumber(),
		// 	gcsResult:       SuccessGCS(),
		// 	weakResults: map[int]utils.FinishedFile{
		// 		LastBuildNumber():     FailGCS(),
		// 		LastBuildNumber() - 1: SuccessGCS(),
		// 		LastBuildNumber() - 2: FailGCS(),
		// 	},
		// 	gcsJunit: map[string][]byte{
		// 		"junit_01.xml": getJUnit(5, 0),
		// 		"junit_02.xml": getJUnit(6, 0),
		// 		"junit_03.xml": getJUnit(7, 0),
		// 	},
		// 	e2ePass:  true,
		// 	unitPass: true,
		// 	reason:   e2eFailure,
		// 	state:    "success",
		// },
	}
	for testNum := range tests {
		test := &tests[testNum]
		issueNum := testNum + 1
		issueNumStr := strconv.Itoa(issueNum)

		test.issue.Number = &issueNum
		client, server, mux := github_test.InitServer(t, test.issue, test.pr, test.events, test.commits, test.ciStatus)

		config := &github_util.Config{}
		config.Org = "o"
		config.Project = "r"
		config.SetClient(client)
		// Don't wait so long for it to go pending or back
		d := 250 * time.Millisecond
		config.PendingWaitTime = &d

		stateSet := ""

		numJenkinsCalls := 0
		// Respond with success to jenkins requests.
		path := "/job/foo/lastCompletedBuild/api/json"
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Errorf("Unexpected method: %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			data, err := json.Marshal(test.jenkinsJob)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			w.Write(data)

			// There is no good spot for this, but this gets called
			// before we queue the PR. So mark the PR as "merged".
			// When the sq initializes, it will check the Jenkins status,
			// so we don't want to modify the PR there. Instead we need
			// to wait until the second time we check Jenkins, which happens
			// we did the IsMerged() check.
			numJenkinsCalls = numJenkinsCalls + 1
			if numJenkinsCalls == 2 && test.mergeAfterQueued {
				test.pr.Merged = boolPtr(true)
				test.pr.Mergeable = nil
			}
		})
		path = "/foo/latest-build.txt"
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Errorf("Unexpected method: %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(strconv.Itoa(test.lastBuildNumber)))
		})
		path = fmt.Sprintf("/foo/%v/finished.json", test.lastBuildNumber)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Errorf("Unexpected method: %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			data, err := json.Marshal(test.gcsResult)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			w.Write(data)

			numJenkinsCalls = numJenkinsCalls + 1
			if numJenkinsCalls == 2 && test.mergeAfterQueued {
				test.pr.Merged = boolPtr(true)
				test.pr.Mergeable = nil
			}
		})
		path = "/bar/latest-build.txt"
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Errorf("Unexpected method: %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(strconv.Itoa(test.lastBuildNumber)))
		})
		for buildNumber := range test.weakResults {
			path = fmt.Sprintf("/bar/%v/finished.json", buildNumber)
			// workaround go for loop semantics
			buildNumberCopy := buildNumber
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("Unexpected method: %s", r.Method)
				}
				w.WriteHeader(http.StatusOK)
				data, err := json.Marshal(test.weakResults[buildNumberCopy])
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				w.Write(data)
			})
		}
		for junitFile, xml := range test.gcsJunit {
			path = fmt.Sprintf("/bar/%v/artifacts/%v", test.lastBuildNumber, junitFile)
			// workaround go for loop semantics
			xmlCopy := xml
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("Unexpected method: %s", r.Method)
				}
				w.WriteHeader(http.StatusOK)
				w.Write(xmlCopy)
			})
		}
		path = fmt.Sprintf("/repos/o/r/issues/%d/comments", issueNum)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				c := new(github.IssueComment)
				json.NewDecoder(r.Body).Decode(c)
				msg := *c.Body
				if strings.HasPrefix(msg, "@"+jenkinsBotName+" test this") {
					go fakeRunGithubE2ESuccess(test.ciStatus, test.e2ePass, test.unitPass)
				}
				w.WriteHeader(http.StatusOK)
				data, err := json.Marshal(github.IssueComment{})
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				w.Write(data)
				return
			}
			if r.Method == "GET" {
				w.WriteHeader(http.StatusOK)
				data, err := json.Marshal([]github.IssueComment{})
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				w.Write(data)
				return
			}
			t.Errorf("Unexpected method: %s", r.Method)
		})
		path = fmt.Sprintf("/repos/o/r/pulls/%d/merge", issueNum)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "PUT" {
				t.Errorf("Unexpected method: %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			data, err := json.Marshal(github.PullRequestMergeResult{})
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			w.Write(data)
			test.pr.Merged = boolPtr(true)
		})
		path = fmt.Sprintf("/repos/o/r/statuses/%s", *test.pr.Head.SHA)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				t.Errorf("Unexpected method: %s", r.Method)
			}
			decoder := json.NewDecoder(r.Body)
			var status github.RepoStatus
			err := decoder.Decode(&status)
			if err != nil {
				t.Errorf("Unable to decode status: %v", err)
			}

			stateSet = *status.State

			data, err := json.Marshal(status)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			w.Write(data)
		})

		sq := getTestSQ(true, config, server)

		obj := github_util.TestObject(config, test.issue, test.pr, test.commits, test.events)
		sq.Munge(obj)
		done := make(chan bool, 1)
		go func(done chan bool) {
			for {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%d:%q panic'd likely writing to 'done' channel", testNum, test.name)
					}
				}()

				if sq.prStatus[issueNumStr].Reason == test.reason {
					done <- true
					return
				}
				found := false
				for _, status := range sq.statusHistory {
					if status.Reason == test.reason {
						found = true
						break
					}
				}
				if found {
					done <- true
					return
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(done)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("%d:%q timed out waiting expected reason=%q but got prStatus:%q history:%v", testNum, test.name, test.reason, sq.prStatus[issueNumStr].Reason, sq.statusHistory)
		}
		close(done)
		server.Close()

		if test.state != "" && test.state != stateSet {
			t.Errorf("%d:%q state set to %q but expected %q", testNum, test.name, stateSet, test.state)
		}
	}
}

func TestCalcMergeRate(t *testing.T) {
	runtime.GOMAXPROCS(runtime.NumCPU())

	tests := []struct {
		name     string // because when it fails, counting is hard
		preRate  float64
		interval time.Duration
		expected func(float64) bool
	}{
		{
			name:     "0One",
			preRate:  0,
			interval: time.Duration(time.Hour),
			expected: func(rate float64) bool {
				return rate > 10 && rate < 11
			},
		},
		{
			name:     "24One",
			preRate:  24,
			interval: time.Duration(time.Hour),
			expected: func(rate float64) bool {
				return rate == float64(24)
			},
		},
		{
			name:     "24Two",
			preRate:  24,
			interval: time.Duration(2 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 17 && rate < 18
			},
		},
		{
			name:     "24HalfHour",
			preRate:  24,
			interval: time.Duration(time.Hour) / 2,
			expected: func(rate float64) bool {
				return rate > 31 && rate < 32
			},
		},
		{
			name:     "24Three",
			preRate:  24,
			interval: time.Duration(3 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 14 && rate < 15
			},
		},
		{
			name:     "24Then24",
			preRate:  24,
			interval: time.Duration(24 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 2 && rate < 3
			},
		},
	}
	for testNum, test := range tests {
		sq := getTestSQ(false, nil, nil)
		clock := sq.clock.(*util.FakeClock)
		sq.mergeRate = test.preRate
		clock.Step(test.interval)
		sq.updateMergeRate()
		if !test.expected(sq.mergeRate) {
			t.Errorf("%d:%s: expected() failed: rate:%v", testNum, test.name, sq.mergeRate)
		}
	}
}

func TestCalcMergeRateWithTail(t *testing.T) {
	runtime.GOMAXPROCS(runtime.NumCPU())

	tests := []struct {
		name     string // because when it fails, counting is hard
		preRate  float64
		interval time.Duration
		expected func(float64) bool
	}{
		{
			name:     "ZeroPlusZero",
			preRate:  0,
			interval: time.Duration(0),
			expected: func(rate float64) bool {
				return rate == float64(0)
			},
		},
		{
			name:     "0OneHour",
			preRate:  0,
			interval: time.Duration(time.Hour),
			expected: func(rate float64) bool {
				return rate == 0
			},
		},
		{
			name:     "TinyOneHour",
			preRate:  .001,
			interval: time.Duration(time.Hour),
			expected: func(rate float64) bool {
				return rate == .001
			},
		},
		{
			name:     "TwentyFourPlusHalfHour",
			preRate:  24,
			interval: time.Duration(time.Hour) / 2,
			expected: func(rate float64) bool {
				return rate == 24
			},
		},
		{
			name:     "TwentyFourPlusOneHour",
			preRate:  24,
			interval: time.Duration(time.Hour),
			expected: func(rate float64) bool {
				return rate == 24
			},
		},
		{
			name:     "TwentyFourPlusTwoHour",
			preRate:  24,
			interval: time.Duration(2 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 17 && rate < 18
			},
		},
		{
			name:     "TwentyFourPlusFourHour",
			preRate:  24,
			interval: time.Duration(4 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 12 && rate < 13
			},
		},
		{
			name:     "TwentyFourPlusTwentyFourHour",
			preRate:  24,
			interval: time.Duration(24 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 2 && rate < 3
			},
		},
		{
			name:     "TwentyFourPlusTiny",
			preRate:  24,
			interval: time.Duration(time.Nanosecond),
			expected: func(rate float64) bool {
				return rate == 24
			},
		},
		{
			name:     "TwentyFourPlusHuge",
			preRate:  24,
			interval: time.Duration(1024 * time.Hour),
			expected: func(rate float64) bool {
				return rate > 0 && rate < 1
			},
		},
	}
	for testNum, test := range tests {
		sq := getTestSQ(false, nil, nil)
		sq.mergeRate = test.preRate
		clock := sq.clock.(*util.FakeClock)
		clock.Step(test.interval)
		rate := sq.calcMergeRateWithTail()
		if !test.expected(rate) {
			t.Errorf("%d:%s: %v", testNum, test.name, rate)
		}
	}
}

func TestHealth(t *testing.T) {
	sq := getTestSQ(false, nil, nil)
	sq.updateHealth()
	sq.updateHealth()
	if len(sq.healthHistory) != 2 {
		t.Errorf("Wrong length healthHistory after calling updateHealth: %v", sq.healthHistory)
	}
	if sq.health.TotalLoops != 2 || sq.health.NumStable != 2 || len(sq.health.NumStablePerJob) != 2 {
		t.Errorf("Wrong number of stable loops after calling updateHealth: %v", sq.health)
	}
	for _, stable := range sq.health.NumStablePerJob {
		if stable != 2 {
			t.Errorf("Wrong number of stable loops for a job: %v", sq.health.NumStablePerJob)
		}
	}
	sq.healthHistory[0].Time = time.Now().AddDate(0, 0, -3)
	sq.healthHistory[1].Time = time.Now().AddDate(0, 0, -2)
	sq.updateHealth()
	if len(sq.healthHistory) != 1 {
		t.Errorf("updateHealth didn't truncate old entries: %v", sq.healthHistory)
	}
}
