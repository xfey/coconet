package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxProjectsPerAccount      = 20
	maxActiveMembersPerProject = 50
	maxPendingAuthorizations   = 1000
)

// Deployment policy stays on the server, separate from client preferences and
// model-result validation. Defaults retain the existing MVP protections.
type deploymentLimits struct {
	ProjectsPerAccount int
	MembersPerProject  int
}

func defaultDeploymentLimits() deploymentLimits {
	return deploymentLimits{maxProjectsPerAccount, maxActiveMembersPerProject}
}

func deploymentLimitsFromEnvironment() (deploymentLimits, error) {
	limits := defaultDeploymentLimits()
	for name, target := range map[string]*int{
		"COCONET_MAX_PROJECTS_PER_ACCOUNT": &limits.ProjectsPerAccount,
		"COCONET_MAX_MEMBERS_PER_PROJECT":  &limits.MembersPerProject,
	} {
		if value := os.Getenv(name); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return limits, fmt.Errorf("%s must be a positive integer", name)
			}
			*target = n
		}
	}
	return limits, nil
}

type requestRate struct {
	count  int
	window time.Duration
}

var requestRates = map[string]requestRate{
	"project.create.ip":       {20, time.Hour},
	"project.create.account":  {10, time.Hour},
	"project.join.ip":         {60, time.Minute},
	"project.preview.account": {60, time.Minute},
	"auth":                    {20, time.Minute},
	"auth/v1/auth/token":      {120, time.Minute},
	"auth.decision":           {30, time.Minute},
	"auth.preview":            {120, time.Minute},
	"dashboard.login":         {10, time.Minute},
	"account.key":             {10, time.Minute},
	"workspace.report":        {60, time.Minute},
	"dashboard.library":       {60, time.Minute},
}

func (limiter *requestLimiter) check(scope, key string) error {
	policy, ok := requestRates[scope]
	if !ok && strings.HasPrefix(scope, "auth/") {
		policy, ok = requestRates["auth"]
	}
	if !ok {
		return fmt.Errorf("unknown request rate policy: %s", scope)
	}
	if wait := limiter.reserve(scope, key, policy.count, policy.window); wait > 0 {
		seconds := int((wait + time.Second - 1) / time.Second)
		err := rateLimitError()
		err.Message = fmt.Sprintf("too many requests for %s; retry after %d seconds", scope, seconds)
		err.RetryAfterSeconds = seconds
		return err
	}
	return nil
}
