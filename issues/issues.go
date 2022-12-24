// Copyright 2017 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package issues

import (
	"fmt"
	"strings"
	"time"

	gojira "github.com/andygrunwald/go-jira"
	gh "github.com/google/go-github/v48/github"

	"github.com/uwu-tools/gh-jira-issue-sync/comments"
	"github.com/uwu-tools/gh-jira-issue-sync/config"
	"github.com/uwu-tools/gh-jira-issue-sync/github"
	"github.com/uwu-tools/gh-jira-issue-sync/jira"
)

// dateFormat is the format used for the Last IS Update field.
const dateFormat = "2006-01-02T15:04:05.0-0700"

// Compare gets the list of GitHub issues updated since the `since` date,
// gets the list of JIRA issues which have GitHub ID custom fields in that list,
// then matches each one. If a JIRA issue already exists for a given GitHub issue,
// it calls UpdateIssue; if no JIRA issue already exists, it calls CreateIssue.
func Compare(cfg config.Config, ghClient github.Client, jiraClient jira.Client) error {
	log := cfg.GetLogger()

	log.Debug("Collecting issues")

	ghIssues, err := ghClient.ListIssues()
	if err != nil {
		return fmt.Errorf("listing GitHub issues: %w", err)
	}

	if len(ghIssues) == 0 {
		log.Info("There are no GitHub issues; exiting")
		return nil
	}

	ids := make([]int, len(ghIssues))
	for i, v := range ghIssues {
		ghID := v.GetID()
		ids[i] = int(ghID)
	}

	jiraIssues, err := jiraClient.ListIssues(ids)
	if err != nil {
		return fmt.Errorf("listing Jira issues: %w", err)
	}

	log.Debug("Collected all JIRA issues")

	for _, ghIssue := range ghIssues {
		found := false
		for _, jIssue := range jiraIssues {
			id, err := jIssue.Fields.Unknowns.Int(cfg.GetFieldKey(config.GitHubID))
			if err != nil {
				return fmt.Errorf("retrieving field key from GitHub ID: %w", err)
			}
			if *ghIssue.ID == id {
				found = true
				if err := UpdateIssue(cfg, ghIssue, jIssue, ghClient, jiraClient); err != nil {
					log.Errorf("Error updating issue %s. Error: %v", jIssue.Key, err)
				}
				break
			}
		}
		if !found {
			if err := CreateIssue(cfg, ghIssue, ghClient, jiraClient); err != nil {
				log.Errorf("Error creating issue for #%d. Error: %v", *ghIssue.Number, err)
			}
		}
	}

	return nil
}

// DidIssueChange tests each of the relevant fields on the provided JIRA and GitHub issue
// and returns whether or not they differ.
func DidIssueChange(cfg config.Config, ghIssue gh.Issue, jIssue gojira.Issue) bool {
	log := cfg.GetLogger()

	log.Debugf("Comparing GitHub issue #%d and JIRA issue %s", ghIssue.GetNumber(), jIssue.Key)

	anyDifferent := false

	anyDifferent = anyDifferent || (ghIssue.GetTitle() != jIssue.Fields.Summary)
	anyDifferent = anyDifferent || (ghIssue.GetBody() != jIssue.Fields.Description)

	key := cfg.GetFieldKey(config.GitHubStatus)
	field, err := jIssue.Fields.Unknowns.String(key)
	if err != nil || *ghIssue.State != field {
		anyDifferent = true
	}

	key = cfg.GetFieldKey(config.GitHubReporter)
	field, err = jIssue.Fields.Unknowns.String(key)
	if err != nil || *ghIssue.User.Login != field {
		anyDifferent = true
	}

	labels := make([]string, len(ghIssue.Labels))
	for i, l := range ghIssue.Labels {
		labels[i] = *l.Name
	}

	key = cfg.GetFieldKey(config.GitHubLabels)
	field, err = jIssue.Fields.Unknowns.String(key)
	if err != nil && strings.Join(labels, ",") != field {
		anyDifferent = true
	}

	log.Debugf("Issues have any differences: %t", anyDifferent)

	return anyDifferent
}

// UpdateIssue compares each field of a GitHub issue to a JIRA issue; if any of them
// differ, the differing fields of the JIRA issue are updated to match the GitHub
// issue.
func UpdateIssue(cfg config.Config, ghIssue gh.Issue, jIssue gojira.Issue, ghClient github.Client, jClient jira.Client) error {
	log := cfg.GetLogger()

	log.Debugf("Updating JIRA %s with GitHub #%d", jIssue.Key, *ghIssue.Number)

	var issue gojira.Issue

	if DidIssueChange(cfg, ghIssue, jIssue) {
		fields := gojira.IssueFields{}
		fields.Unknowns = map[string]interface{}{}

		fields.Summary = ghIssue.GetTitle()
		fields.Description = ghIssue.GetBody()
		fields.Unknowns[cfg.GetFieldKey(config.GitHubStatus)] = ghIssue.GetState()
		fields.Unknowns[cfg.GetFieldKey(config.GitHubReporter)] = ghIssue.User.GetLogin()

		labels := make([]string, len(ghIssue.Labels))
		for i, l := range ghIssue.Labels {
			labels[i] = l.GetName()
		}
		fields.Unknowns[cfg.GetFieldKey(config.GitHubLabels)] = strings.Join(labels, ",")

		fields.Unknowns[cfg.GetFieldKey(config.LastISUpdate)] = time.Now().Format(dateFormat)

		fields.Type = jIssue.Fields.Type

		issue = gojira.Issue{
			Fields: &fields,
			Key:    jIssue.Key,
			ID:     jIssue.ID,
		}

		var err error
		issue, err = jClient.UpdateIssue(issue)
		if err != nil {
			return fmt.Errorf("updating Jira issue: %w", err)
		}

		log.Debugf("Successfully updated JIRA issue %s!", jIssue.Key)
	} else {
		log.Debugf("JIRA issue %s is already up to date!", jIssue.Key)
	}

	issue, err := jClient.GetIssue(jIssue.Key)
	if err != nil {
		return fmt.Errorf("getting Jira issue %s: %w", jIssue.Key, err)
	}

	if err := comments.Compare(cfg, ghIssue, issue, ghClient, jClient); err != nil {
		return fmt.Errorf("comparing comments for issue %s: %w", jIssue.Key, err)
	}

	return nil
}

// CreateIssue generates a JIRA issue from the various fields on the given GitHub issue, then
// sends it to the JIRA API.
func CreateIssue(cfg config.Config, issue gh.Issue, ghClient github.Client, jClient jira.Client) error {
	log := cfg.GetLogger()

	log.Debugf("Creating JIRA issue based on GitHub issue #%d", *issue.Number)

	fields := gojira.IssueFields{
		Type: gojira.IssueType{
			Name: "Task", // TODO: Determine issue type
		},
		Project:     cfg.GetProject(),
		Summary:     issue.GetTitle(),
		Description: issue.GetBody(),
		Unknowns:    map[string]interface{}{},
	}

	fields.Unknowns[cfg.GetFieldKey(config.GitHubID)] = issue.GetID()
	fields.Unknowns[cfg.GetFieldKey(config.GitHubNumber)] = issue.GetNumber()
	fields.Unknowns[cfg.GetFieldKey(config.GitHubStatus)] = issue.GetState()
	fields.Unknowns[cfg.GetFieldKey(config.GitHubReporter)] = issue.User.GetLogin()

	strs := make([]string, len(issue.Labels))
	for i, v := range issue.Labels {
		strs[i] = *v.Name
	}
	fields.Unknowns[cfg.GetFieldKey(config.GitHubLabels)] = strings.Join(strs, ",")

	fields.Unknowns[cfg.GetFieldKey(config.LastISUpdate)] = time.Now().Format(dateFormat)

	jIssue := gojira.Issue{
		Fields: &fields,
	}

	jIssue, err := jClient.CreateIssue(jIssue)
	if err != nil {
		return fmt.Errorf("creating Jira issue: %w", err)
	}

	jIssue, err = jClient.GetIssue(jIssue.Key)
	if err != nil {
		return fmt.Errorf("getting Jira issue %s: %w", jIssue.Key, err)
	}

	log.Debugf("Created JIRA issue %s!", jIssue.Key)

	if err := comments.Compare(cfg, issue, jIssue, ghClient, jClient); err != nil {
		return fmt.Errorf("comparing comments for issue %s: %w", jIssue.Key, err)
	}

	return nil
}
