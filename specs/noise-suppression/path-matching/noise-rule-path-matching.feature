@domain:noise-suppression @area:path-matching @from:noise/noise
Feature: Targeting noise rules at locations in a response
  Lets operators aim one rule at a status, a header or a field nested anywhere in a JSON body using a glob, so one line covers a timestamp wherever it appears.

  Background:
    Given a noise rule that ignores <pattern>

  @id:noise-suppression.path-matching.001 @rule:noise.paths.body-keys-match-case-insensitively @inferred @source:internal/noise/noise.go#matchGlob @questionable
  Scenario Outline: A rule for a body field also covers fields whose names differ only in case
    Given a primary and a shadow response that differ at /body/UpdatedAt
    When the pair is compared
    Then the difference at /body/UpdatedAt is suppressed

    Examples:
      | pattern         |
      | /body/updatedAt |
