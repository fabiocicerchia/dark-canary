@domain:configuration-and-lifecycle @area:health-and-shutdown @from:cmd/dark-canary
Feature: Health checks and shutdown
  Orchestrators need a liveness probe and a clean stop, and operators need the final report when the process ends.

  Background:
    Given dark-canary is running

  @id:configuration-and-lifecycle.health-and-shutdown.001 @rule:lifecycle.health.healthz-open @gap @source:cmd/dark-canary/main.go#routes
  Scenario: The health check answers without a token
    Given the collector was started with a token
    When a probe calls GET /healthz on the collector address without a token
    Then the response status is 200
    And the body is "ok"

  @id:configuration-and-lifecycle.health-and-shutdown.002 @rule:lifecycle.shutdown.final-report-on-signal @gap @source:cmd/dark-canary/main.go#run
  Scenario: On SIGTERM or Ctrl-C the process prints the final report and exits cleanly
    Given pairs have been compared since startup
    When the operator sends SIGTERM to dark-canary
    Then the listeners stop accepting new connections
    And the final text report is printed to stderr
    And the process exits with code 0 without waiting for in-flight requests to finish
