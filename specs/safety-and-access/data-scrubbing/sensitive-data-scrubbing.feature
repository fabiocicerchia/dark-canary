@domain:safety-and-access @area:data-scrubbing @from:safety/safety
Feature: Scrubbing sensitive data from captures
  Serves the operator and their users by making sure personal data and credentials in mirrored traffic never land raw in the collector, the report or the dashboard.

  Background:
    Given the collector is running

  @id:safety-and-access.data-scrubbing.004 @rule:safety.scrubbing.object-and-array-values-not-redacted @inferred @source:internal/safety/safety.go#NewScrubber @questionable
  Scenario: A configured field holding an object or a list is not redacted
    Given the operator has configured the scrub field "email"
    When the edge node sends a capture whose body is {"email":["jo@example.com","jo@example.org"]}
    Then the stored body still contains "jo@example.com" and "jo@example.org"

  @id:safety-and-access.data-scrubbing.005 @rule:safety.scrubbing.escaped-quotes-do-not-swallow-body @verified @source:internal/safety/safety.go#NewScrubber @test:internal/safety/safety_test.go#TestScrubberHandlesEscapedQuotesWithoutRunningOn
  Scenario: Redacting a string with escaped quotes leaves the rest of the body intact
    Given the operator has configured the scrub field "note"
    When the edge node sends a capture whose body is {"note":"say \"hi\"","after":"visible"}
    Then the stored body has "note" set to "[redacted]"
    And the stored body still has "after" set to "visible"
